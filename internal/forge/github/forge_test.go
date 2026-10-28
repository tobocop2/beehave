package github_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/forge/github"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

func TestPRList_ReturnsAListUnderItsLimit(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, `[{"number":1},{"number":2}]`)

	rows, err := f.PRList(context.Background(), 3, "--state", "open", "--json", "number")
	require.NoError(t, err)
	assert.Equal(t, []model.PullRequest{{Number: 1}, {Number: 2}}, rows)
	assert.Equal(t, [][]string{{"pr", "list", "--repo", repo, "--limit", "3", "--state", "open", "--json", "number"}}, run.Calls)
}

func TestPRList_TakesAListOneUnderItsLimit(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, `[{"number":1},{"number":2},{"number":3}]`)

	rows, err := f.PRList(context.Background(), 4, "--json", "number")
	require.NoError(t, err)
	assert.Len(t, rows, 3)
}

func TestPRList_Refusals(t *testing.T) {
	t.Parallel()

	const parse = "cannot parse the pull request list (--json number)"

	tests := []struct {
		name   string
		stdout string
		fail   bool
		limit  int
		want   string
	}{
		{
			"a_list_that_fills_the_limit_may_be_cut_off", `[{"number":1},{"number":2},{"number":3}]`, false, 3,
			"the pull request list returned 3 of a limit of 3, so it may be cut off (--json number). Raise the limit.",
		},
		{"a_failed_call_is_never_an_empty_list", "", true, 3, "cannot list pull requests (--json number)"},
		{"not_json", "not json", false, 3, parse},
		{"empty_answer", "", false, 3, parse},
		{"an_object", `{"a": 1}`, false, 3, parse},
		{"null", "null", false, 3, parse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, _ := fixed(t, tt.stdout)
			if tt.fail {
				f, _ = failing(t, "HTTP 502")
			}

			_, err := f.PRList(context.Background(), tt.limit, "--json", "number")

			var refused *forge.Error

			require.ErrorAs(t, err, &refused)
			assert.Equal(t, forge.CodeRefused, refused.Code)
			assert.True(t, strings.HasPrefix(refused.Message, tt.want), refused.Message)
		})
	}
}

func TestPRList_NamesTheCommandWhenTheShapeIsWrong(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, "null")

	_, err := f.PRList(context.Background(), 3, "--json", "number")
	require.ErrorIs(t, err, model.ErrNullAnswer)
	assert.Equal(t, "cannot parse the pull request list (--json number): cannot read the answer of `gh pr list` (the answer is null)", err.Error())
}

func thread(resolved any, path any, line any, comments ...fields) fields {
	return fields{"isResolved": resolved, "path": path, "line": line, "comments": fields{"nodes": comments}}
}

func TestUnresolvedThreads_ListsPathLineAndFirstComment(t *testing.T) {
	t.Parallel()

	first := fields{"author": fields{"login": "gz"}, "body": "please fix"}
	f, run := fixed(t, graph(t, fields{"reviewThreads": fields{"totalCount": 2, "nodes": []fields{
		thread(true, "a.rs", 1, fields{"author": fields{"login": "gz"}, "body": "fine"}),
		thread(false, "b.rs", 9, first),
	}}}))

	threads, err := f.UnresolvedThreads(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, threads, 1)
	assert.Equal(t, "b.rs:9 gz: please fix", threads[0].Summary())

	call := run.Calls[0]
	assert.Equal(t, []string{"api", "graphql", "-f", "o=owner", "-f", "n=name", "-F", "p=7", "-f"}, call[:9])
	assert.True(t, strings.HasPrefix(call[9], "query=query($o:String!,$n:String!,$p:Int!)"))
}

func TestUnresolvedThreads_CutsTheCommentAt80Characters(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("é", 80) + "TAIL"
	f, _ := fixed(t, graph(t, fields{"reviewThreads": fields{"totalCount": 1, "nodes": []fields{
		thread(false, "p", 1, fields{"author": fields{"login": "u"}, "body": body}),
	}}}))

	threads, err := f.UnresolvedThreads(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, "p:1 u: "+strings.Repeat("é", 80), threads[0].Summary())
}

func TestUnresolvedThreads_PrintsNullForMissingParts(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, graph(t, fields{"reviewThreads": fields{"totalCount": 1, "nodes": []fields{thread(false, nil, nil)}}}))

	threads, err := f.UnresolvedThreads(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, "null:null null: null", threads[0].Summary())
}

func TestUnresolvedThreads_ATheadWithNoResolvedFlagStaysListed(t *testing.T) {
	t.Parallel()

	for _, flag := range []fields{{}, {"isResolved": nil}} {
		node := fields{"path": "a.go", "line": 3, "comments": fields{"nodes": []fields{}}}
		for key, value := range flag {
			node[key] = value
		}

		f, _ := fixed(t, graph(t, fields{"reviewThreads": fields{"totalCount": 1, "nodes": []fields{node}}}))

		threads, err := f.UnresolvedThreads(context.Background(), 7)
		require.NoError(t, err)
		assert.Equal(t, "a.go:3 null: null", threads[0].Summary())
	}
}

func TestUnresolvedThreads_TakesACountEqualToThePage(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, graph(t, fields{"reviewThreads": fields{"totalCount": 1, "nodes": []fields{{"isResolved": true}}}}))

	threads, err := f.UnresolvedThreads(context.Background(), 7)
	require.NoError(t, err)
	assert.Empty(t, threads)
}

func TestUnresolvedThreads_Refusals(t *testing.T) {
	t.Parallel()

	const base = "cannot read the review threads of pull request 7"

	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{
			"more_threads_than_one_page", graph(t, fields{"reviewThreads": fields{"totalCount": 150, "nodes": []fields{}}}),
			"pull request 7 has 150 review threads, more than one page; the rest are unknown, not resolved",
		},
		{"an_empty_object", "{}", base + ": the answer holds none"},
		{"no_threads", graph(t, fields{"reviewThreads": nil}), base + ": the answer holds none"},
		{"no_repository", `{"data":{"repository":null}}`, base + ": the answer holds none"},
		{"no_data", `{"data":null}`, base + ": the answer holds none"},
		{"null", "null", base + ": cannot read the answer of `gh api graphql` (the answer is null)"},
		{"not_json", "not json", base + ": cannot read the answer of `gh api graphql`"},
		{"data_is_a_number", `{"data": 7}`, base + ": cannot read the answer of `gh api graphql`"},
		{"no_node_list", graph(t, fields{"reviewThreads": fields{}}), base + ": the answer holds no thread list"},
		{"null_node_list", graph(t, fields{"reviewThreads": fields{"nodes": nil}}), base + ": the answer holds no thread list"},
		{"count_without_nodes", graph(t, fields{"reviewThreads": fields{"totalCount": 0}}), base + ": the answer holds no thread list"},
		{"nodes_is_a_string", graph(t, fields{"reviewThreads": fields{"nodes": "x"}}), base + ": cannot read the answer"},
		{"nodes_is_an_object", graph(t, fields{"reviewThreads": fields{"nodes": fields{"a": 1}}}), base + ": cannot read the answer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, _ := fixed(t, tt.stdout)

			_, err := f.UnresolvedThreads(context.Background(), 7)
			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), tt.want), err.Error())
		})
	}
}

func TestUnresolvedThreads_AFailedReadIsRefusedAndNeverEmpty(t *testing.T) {
	t.Parallel()

	f, _ := failing(t, "")

	_, err := f.UnresolvedThreads(context.Background(), 7)
	require.EqualError(t, err, "cannot read the review threads of pull request 7")
}

func TestReviews_JoinsEveryPage(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, `[[{"state":"APPROVED"},{"state":"COMMENTED"}],[{"state":"PENDING"}]]`)

	reviews, err := f.Reviews(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, []model.ReviewState{model.ReviewApproved, model.ReviewCommented, model.ReviewPending},
		[]model.ReviewState{reviews[0].State, reviews[1].State, reviews[2].State})
	assert.Equal(t, [][]string{{"api", "--paginate", "--slurp", "repos/owner/name/pulls/5/reviews"}}, run.Calls)
}

func TestReviews_NoReviewsIsAnEmptyList(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"[]", "[[]]"} {
		f, _ := fixed(t, text)

		reviews, err := f.Reviews(context.Background(), 5)
		require.NoError(t, err)
		assert.Empty(t, reviews)
	}
}

func TestReviews_AnUnreadableAnswerIsRefused(t *testing.T) {
	t.Parallel()

	const base = "could not read the reviews of pull request 5"

	for _, text := range []string{"", "not json", `{"a":1}`, "[1]", "null"} {
		f, _ := fixed(t, text)

		_, err := f.Reviews(context.Background(), 5)
		require.Error(t, err, text)
		assert.True(t, strings.HasPrefix(err.Error(), base), err.Error())
	}

	f, _ := failing(t, "")

	_, err := f.Reviews(context.Background(), 5)
	require.EqualError(t, err, base)

	_, err = newForge(t, repo, exitOne{}).Reviews(context.Background(), 5)
	require.EqualError(t, err, base)
}

// exitOne answers a valid list with exit code 1.
type exitOne struct{}

func (exitOne) Run(context.Context, []string) model.CommandResult {
	return model.CommandResult{ExitCode: 1, Stdout: "[[]]"}
}

func nodes(t *testing.T, total int, rows ...[3]any) string {
	t.Helper()

	items := []fields{}

	for _, row := range rows {
		item := fields{"state": row[1]}
		if row[0] != nil {
			item["author"] = fields{"login": row[0]}
		}

		if row[2] != nil {
			item["commit"] = fields{"oid": row[2]}
		}

		items = append(items, item)
	}

	if total < 0 {
		total = len(items)
	}

	return graph(t, fields{"latestOpinionatedReviews": fields{"totalCount": total, "nodes": items}})
}

func TestOpenChangeRequests_AreLoginAtNineCharactersOfTheSha(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, nodes(t, -1,
		[3]any{"alice", "CHANGES_REQUESTED", "0123456789abcdef"},
		[3]any{"bob", "APPROVED", "ffffffffffff"},
		[3]any{"erin", "COMMENTED", "ffffffffffff"},
		[3]any{"fay", "DISMISSED", "ffffffffffff"},
		[3]any{"carol", "CHANGES_REQUESTED", "012345678"},
		[3]any{"dave", "CHANGES_REQUESTED", "01234567"},
	))

	standing, err := f.OpenChangeRequests(context.Background(), 9)
	require.NoError(t, err)
	assert.Equal(t, "alice@012345678,carol@012345678,dave@01234567", model.JoinChangeRequests(standing))
	assert.Equal(t, []string{"-f", "o=owner", "-f", "n=name", "-F", "p=9"}, run.Calls[0][2:8])
}

func TestOpenChangeRequests_NoneStandingIsAnEmptyList(t *testing.T) {
	t.Parallel()

	for _, text := range []string{nodes(t, -1, [3]any{"bob", "APPROVED", "abc"}), nodes(t, -1)} {
		f, _ := fixed(t, text)

		standing, err := f.OpenChangeRequests(context.Background(), 9)
		require.NoError(t, err)
		assert.Empty(t, standing)
	}
}

func TestOpenChangeRequests_AMissingAuthorOrCommitReadsAsEmpty(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, nodes(t, -1, [3]any{nil, "CHANGES_REQUESTED", "abcdefghijk"}, [3]any{"zed", "CHANGES_REQUESTED", nil}))

	standing, err := f.OpenChangeRequests(context.Background(), 9)
	require.NoError(t, err)
	assert.Equal(t, "@abcdefghi,zed@", model.JoinChangeRequests(standing))
}

func TestOpenChangeRequests_TakesACountEqualToThePage(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, nodes(t, 1, [3]any{"a", "APPROVED", "x"}))

	standing, err := f.OpenChangeRequests(context.Background(), 9)
	require.NoError(t, err)
	assert.Empty(t, standing)
}

func TestOpenChangeRequests_Refusals(t *testing.T) {
	t.Parallel()

	const base = "could not read the reviews of pull request 9"

	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{
			"more_reviewers_than_one_page", nodes(t, 2, [3]any{"a", "APPROVED", "x"}),
			"pull request 9 has reviews from more than one page of reviewers; the rest are unknown",
		},
		{"an_empty_object", "{}", base + ": the answer holds none"},
		{"no_review_list", graph(t, fields{"latestOpinionatedReviews": nil}), base + ": the answer holds none"},
		{"null", "null", base + ": cannot read the answer of `gh api graphql`"},
		{"not_json", "not json", base + ": cannot read the answer of `gh api graphql`"},
		{"no_node_list", graph(t, fields{"latestOpinionatedReviews": fields{"totalCount": 0}}), base + ": the answer holds no review list"},
		{"null_node_list", graph(t, fields{"latestOpinionatedReviews": fields{"nodes": nil}}), base + ": the answer holds no review list"},
		{"nodes_is_a_string", graph(t, fields{"latestOpinionatedReviews": fields{"nodes": "x"}}), base + ": cannot read the answer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, _ := fixed(t, tt.stdout)

			_, err := f.OpenChangeRequests(context.Background(), 9)
			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), tt.want), err.Error())
		})
	}

	f, _ := failing(t, "")

	_, err := f.OpenChangeRequests(context.Background(), 9)
	require.EqualError(t, err, base)
}

func TestPRChecks_ReturnsRows(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, `[{"name":"ci","bucket":"pass","workflow":"w","link":"l"}]`)

	rows, err := f.PRChecks(context.Background(), 4)
	require.NoError(t, err)
	assert.Equal(t, []model.Check{{Name: "ci", Bucket: model.CheckPass, Workflow: "w", Link: "l"}}, rows)
	assert.Equal(t, [][]string{{"pr", "checks", "4", "--repo", repo, "--json", "name,bucket,workflow,link"}}, run.Calls)
}

func TestPRChecks_AFailureReadsAsNoChecksOnlyForTheNoChecksLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{"the_no_checks_line", "no checks reported on the 'feature' branch\n", ""},
		{"the_line_after_another_line", "warning\nno checks reported on the 'x' branch\n", ""},
		{"the_words_inside_another_line", "error: no checks reported on the 'x' branch\n", "cannot read the checks of pull request 4: error: no checks reported on the 'x' branch"},
		{"any_other_failure_names_its_first_line", "HTTP 502\nsecond line\n", "cannot read the checks of pull request 4: HTTP 502"},
		{"no_error_text", "", "cannot read the checks of pull request 4: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, _ := failing(t, tt.stderr)

			rows, err := f.PRChecks(context.Background(), 4)
			if tt.want == "" {
				require.NoError(t, err)
				assert.NotNil(t, rows)
				assert.Empty(t, rows)

				return
			}

			require.EqualError(t, err, tt.want)
		})
	}
}

func TestPRChecks_RefusesAnAnswerThatIsNotAList(t *testing.T) {
	t.Parallel()

	for _, text := range []string{`{"a":1}`, "null", "", `"abc"`, "7", "true"} {
		f, _ := fixed(t, text)

		_, err := f.PRChecks(context.Background(), 4)
		require.Error(t, err, text)
		assert.True(t, strings.HasPrefix(err.Error(), "cannot read the checks of pull request 4: cannot read the answer of `gh pr checks`"), err.Error())
	}
}

func TestStackID(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, graph(t, fields{"stack": fields{"id": "S_1"}}))

	id, err := f.StackID(context.Background(), 3)
	require.NoError(t, err)
	assert.Equal(t, "S_1", id)
	assert.Equal(t, []string{"-f", "o=owner", "-f", "n=name", "-F", "p=3"}, run.Calls[0][2:8])

	for _, pull := range []fields{{"stack": nil}, {}} {
		f, _ = fixed(t, graph(t, pull))

		id, err = f.StackID(context.Background(), 3)
		require.NoError(t, err)
		assert.Empty(t, id)
	}
}

func TestStackID_Refusals(t *testing.T) {
	t.Parallel()

	const base = "cannot read the stack of pull request 3"

	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{"a_missing_pull_request", graph(t, nil), base + ": no such pull request"},
		{"an_empty_object", "{}", base + ": no such pull request"},
		{"data_is_null", `{"data":null}`, base + ": no such pull request"},
		{"not_json", "not json", base + ": cannot read the answer"},
		{"null", "null", base + ": cannot read the answer"},
		{"an_id_that_is_a_number", graph(t, fields{"stack": fields{"id": 4}}), base + ": cannot read the answer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, _ := fixed(t, tt.stdout)

			_, err := f.StackID(context.Background(), 3)
			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), tt.want), err.Error())
		})
	}

	f, _ := failing(t, "")

	_, err := f.StackID(context.Background(), 3)
	require.EqualError(t, err, base)
}

func TestPRBody(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "Closes #4\r\n")

	body, err := f.PRBody(context.Background(), 8)
	require.NoError(t, err)
	assert.Equal(t, "Closes #4\r\n", body)
	assert.Equal(t, [][]string{{"pr", "view", "8", "-R", repo, "--json", "body", "--jq", ".body"}}, run.Calls)

	f, _ = failing(t, "")

	_, err = f.PRBody(context.Background(), 8)
	require.EqualError(t, err, "cannot read the body of pull request 8 in owner/name, so the Closes lines at the first review are unknown.")
}

func TestPRView(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, `{"isDraft":true,"headRefOid":"abc","author":{"login":"gz"},"labels":[{"name":"x"}],"commits":[{"oid":"1","authors":[{"email":"e","login":"l"}]}]}`)

	view, err := f.PRView(context.Background(), 6, "isDraft", "headRefOid")
	require.NoError(t, err)
	require.NotNil(t, view.IsDraft)
	assert.True(t, *view.IsDraft)
	require.NotNil(t, view.HeadRefOid)
	assert.Equal(t, "abc", *view.HeadRefOid)
	assert.Equal(t, "gz", view.Author.Login)
	require.Len(t, view.Labels, 1)
	assert.Equal(t, "x", view.Labels[0].Name)
	assert.Equal(t, "l", view.Commits[0].Authors[0].Login)
	assert.Equal(t, [][]string{{"pr", "view", "6", "--repo", repo, "--json", "isDraft,headRefOid"}}, run.Calls)

	f, _ = failing(t, "")

	_, err = f.PRView(context.Background(), 6, "isDraft")
	require.EqualError(t, err, "cannot read pull request 6 (isDraft)")

	f, _ = fixed(t, `{"isDraft":"yes"}`)

	_, err = f.PRView(context.Background(), 6, "isDraft")
	require.ErrorContains(t, err, "cannot read pull request 6 (isDraft): cannot read the answer of `gh pr view`")
}

func TestNewExec_RunsTheGhBinaryOnPath(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho '[[{\"state\": \"APPROVED\"}]]'\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700)) //nolint:gosec // a test script must be executable
	t.Setenv("PATH", dir+":/bin:/usr/bin")

	reviews, err := newExec(t).Reviews(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, model.ReviewApproved, reviews[0].State)
}

func TestPRView_AGateFieldThatIsAbsentOrNullIsNilNotFalse(t *testing.T) {
	t.Parallel()

	for _, text := range []string{`{}`, `{"isDraft":null,"state":null,"mergeable":null,"baseRefName":null,"headRefName":null,"headRefOid":null}`} {
		f, _ := fixed(t, text)

		view, err := f.PRView(context.Background(), 6, "isDraft", "state")
		require.NoError(t, err)
		assert.Nil(t, view.IsDraft)
		assert.Nil(t, view.State)
		assert.Nil(t, view.Mergeable)
		assert.Nil(t, view.BaseRefName)
		assert.Nil(t, view.HeadRefName)
		assert.Nil(t, view.HeadRefOid)
	}
}

func TestPRView_DecodesTheFieldsCallersRead(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, `{"mergedAt":"2026-10-02T00:00:00Z","headRepository":{"id":"R_1","name":"obee"},
		"state":"MERGED","mergeable":"UNKNOWN","baseRefName":"main","headRefName":"feat",
		"comments":[{"author":{"login":"gz"},"body":"hi","createdAt":"2026-10-01T00:00:00Z"},{"author":null,"body":"x","createdAt":null}],
		"reviews":[{"author":{"login":"ann"},"body":"ok","state":"APPROVED","submittedAt":"2026-10-01T01:00:00Z"},{"author":{"login":"bob"},"body":"","state":"PENDING","submittedAt":null}]}`)

	view, err := f.PRView(context.Background(), 6, "mergedAt", "headRepository", "comments", "reviews")
	require.NoError(t, err)
	require.NotNil(t, view.MergedAt)
	assert.Equal(t, "2026-10-02T00:00:00Z", *view.MergedAt)
	require.NotNil(t, view.HeadRepository)
	assert.Equal(t, "R_1", view.HeadRepository.ID)
	assert.Equal(t, "obee", view.HeadRepository.Name)
	assert.Equal(t, "MERGED", *view.State)
	assert.Equal(t, "UNKNOWN", *view.Mergeable)
	assert.Equal(t, "main", *view.BaseRefName)
	assert.Equal(t, "feat", *view.HeadRefName)
	require.Len(t, view.Comments, 2)
	assert.Equal(t, "gz", view.Comments[0].Author.Login)
	assert.Equal(t, "hi", view.Comments[0].Body)
	assert.Equal(t, "2026-10-01T00:00:00Z", *view.Comments[0].CreatedAt)
	assert.Nil(t, view.Comments[1].CreatedAt)
	require.Len(t, view.Reviews, 2)
	assert.Equal(t, model.ReviewApproved, view.Reviews[0].State)
	assert.Equal(t, "2026-10-01T01:00:00Z", *view.Reviews[0].SubmittedAt)
	assert.Nil(t, view.Reviews[1].SubmittedAt)

	open, _ := fixed(t, `{"mergedAt":null,"headRepository":null}`)

	view, err = open.PRView(context.Background(), 6, "mergedAt", "headRepository")
	require.NoError(t, err)
	assert.Nil(t, view.MergedAt)
	assert.Nil(t, view.HeadRepository)
}

func TestNew_RefusesARepositoryThatIsNotOwnerSlashName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "owner", "owner/", "/name", "a/b/c", "o wner/name", "owner/na;me", "owner/na me"} {
		f, err := github.New(name, &proctest.Scripted{})
		require.Nil(t, f, name)
		require.EqualError(t, err, "REPO is not set to owner/name. Export it, or put it in round.env.", name)
	}

	f, err := github.New("my-org_1/repo.name", &proctest.Scripted{})
	require.NoError(t, err)
	assert.NotNil(t, f)

	_, err = github.NewExec("nope")
	require.Error(t, err)
}

func TestReviews_ANullPageOrANullItemIsRefusedNotReadAsEmpty(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"a_null_page":                   "[null]",
		"a_null_page_after_a_good_page": "[[],null]",
		"a_null_item":                   "[[null]]",
		"a_null_item_after_a_good_item": `[[{"state":"APPROVED"},null]]`,
	}
	for name, text := range tests {
		f, _ := fixed(t, text)

		_, err := f.Reviews(context.Background(), 5)
		require.Error(t, err, name)
		assert.True(t, strings.HasPrefix(err.Error(), "could not read the reviews of pull request 5: cannot read the answer of `gh api"), err.Error())
	}
}

func newExec(t *testing.T) *github.Forge {
	t.Helper()

	f, err := github.NewExec(repo)
	require.NoError(t, err)

	return f
}

func TestUnresolvedThreads_ANullCommentBodyPrintsNull(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, graph(t, fields{"reviewThreads": fields{"totalCount": 1, "nodes": []fields{
		thread(false, "p", 1, fields{"author": fields{"login": "u"}, "body": nil}),
	}}}))

	threads, err := f.UnresolvedThreads(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, "p:1 u: null", threads[0].Summary())
}
