package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
)

const ghRepo = "owner/name"

func newGithub(t *testing.T) (*store.GithubStore, *scripted) {
	t.Helper()

	run := &scripted{}
	gh, err := store.NewGithubStore(ghRepo, run)
	require.NoError(t, err)

	return gh, run
}

func codeOf(t *testing.T, err error) int {
	t.Helper()

	var failure *store.Error
	require.ErrorAs(t, err, &failure)

	return failure.Code
}

func issue(number int, title string, labels ...string) map[string]any {
	names := make([]map[string]string, len(labels))
	for i, label := range labels {
		names[i] = map[string]string{"name": label}
	}

	return map[string]any{"number": number, "title": title, "labels": names, "assignees": []any{}}
}

func TestNewGithubStore_NeedsOwnerAndName(t *testing.T) {
	t.Parallel()

	for _, repo := range []string{"", "name", "/name", "owner/", "a/b/c", `o/"x`, "o/n m", `o/n\m`, "o/n;m", "o/é", "o w/n"} {
		_, err := store.NewGithubStore(repo, &scripted{})
		require.Error(t, err, repo)
		assert.Equal(t, store.CodeRefused, codeOf(t, err))
		assert.Contains(t, err.Error(), "REPO is not set to owner/name")
	}
}

func TestNewGithubStore_TakesLettersDigitsDotsUnderscoresAndHyphens(t *testing.T) {
	t.Parallel()

	for _, repo := range []string{"tobocop2/obee", "A.b_c-1/Z.y_x-0"} {
		_, err := store.NewGithubStore(repo, &scripted{})
		require.NoError(t, err, repo)
	}
}

func TestGithubOpenItems_SkipsEpicsAndReadsLabels(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	crowded := issue(3, "has two", "P2", "P1", "hold")
	crowded["assignees"] = []map[string]string{{"login": "ann"}, {"login": "bob"}}
	run.plan(toJSON(t, []any{
		issue(1, "plain"), issue(2, "epic: big", "P0"), crowded,
		issue(4, "holding", "holder", "P3"), issue(5, "not an epic: x", "P1"), issue(6, "no hold", "Hold", "P4"),
	}), "issue", "list")

	items, err := gh.OpenItems(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []model.Item{
		{ID: "1", Severity: model.SeverityUnknown, Title: "plain"},
		{ID: "3", Severity: model.SeverityP2, Hold: true, Assignee: "ann,bob", Title: "has two"},
		{ID: "4", Severity: model.SeverityP3, Title: "holding"},
		{ID: "5", Severity: model.SeverityP1, Title: "not an epic: x"},
		{ID: "6", Severity: model.SeverityUnknown, Title: "no hold"},
	}, items)
	assert.Equal(t, []string{"issue", "list", "--repo", ghRepo, "--state", "open", "--limit", "1000", "--json", "number,title,labels,assignees"}, run.calls[0])
}

func TestGithubContainers_ListsOnlyEpics(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.plan(toJSON(t, []any{issue(1, "plain"), issue(2, "epic: big"), issue(9, "epic:x")}), "issue", "list")

	got, err := gh.Containers(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"2", "9"}, got)
}

func TestGithubReads_RefuseWhatTheyCannotTrust(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stdout string
		fail   bool
		want   string
	}{
		{name: "failed_list", fail: true, want: "cannot list the open issues"},
		{name: "full_page", stdout: toJSON(t, fill(1000, nil)), want: "1000 rows, the list may be cut off"},
		{name: "not_a_list", stdout: `{"number":1}`, want: "cannot read the answer of `gh issue list`"},
		{name: "null_answer", stdout: "null", want: "the answer is null"},
		{name: "text_after_the_answer", stdout: "[] []", want: "after top-level value"},
		{name: "element_that_is_not_an_object", stdout: `[3]`, want: "cannot read the answer"},
		{name: "no_title", stdout: `[{"number":1}]`, want: "no readable title"},
		{name: "no_number", stdout: `[{"title":"t"}]`, want: "no readable number"},
		{name: "number_as_text", stdout: `[{"number":"1","title":"t"}]`, want: "cannot read the answer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reads := map[string]func(*store.GithubStore) error{
				"queue":      func(gh *store.GithubStore) error { _, err := gh.OpenItems(context.Background()); return err },
				"containers": func(gh *store.GithubStore) error { _, err := gh.Containers(context.Background()); return err },
			}
			for name, read := range reads {
				gh, run := newGithub(t)
				if tt.fail {
					run.planFail("boom", "issue", "list")
				} else {
					run.plan(tt.stdout, "issue", "list")
				}

				err := read(gh)
				require.Error(t, err, name)
				assert.Contains(t, err.Error(), tt.want, name)
				assert.Equal(t, store.CodeRefused, codeOf(t, err))
			}
		})
	}
}

func TestGithubOpenItems_ListsAPageOneUnderFull(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.plan(toJSON(t, fill(999, nil)), "issue", "list")

	items, err := gh.OpenItems(context.Background())
	require.NoError(t, err)
	assert.Len(t, items, 999)
}

func TestGithubOpenItems_NullLabelsAndAssigneesAreEmpty(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.plan(`[{"number":7,"title":"t","labels":null,"assignees":null}]`, "issue", "list")

	items, err := gh.OpenItems(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []model.Item{{ID: "7", Severity: model.SeverityUnknown, Title: "t"}}, items)
}

func children(total string, nodes string) string {
	return `{"data":{"repository":{"issue":{"subIssues":{"totalCount":` + total + `,"nodes":` + nodes + `}}}}}`
}

func TestGithubChildren_ListsOnlyOpenSubIssues(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.plan(children("100", `[{"number":4,"state":"OPEN"},{"number":5,"state":"CLOSED"},{"number":6,"state":"open"},{"number":7,"state":"OPEN"}]`), "api", "graphql")

	got, err := gh.Children(context.Background(), "3")
	require.NoError(t, err)
	assert.Equal(t, []string{"4", "7"}, got)
	assert.Contains(t, run.calls[0][3], `issue(number:3){subIssues(first:100)`)
	assert.Contains(t, run.calls[0][3], `owner:"owner",name:"name"`)
}

func TestGithubChildren_RefuseToRank(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		container string
		stdout    string
		fail      bool
		want      string
	}{
		{name: "more_than_one_page", container: "3", stdout: children("101", `[]`), want: "has 101 children, more than one page"},
		{name: "no_count", container: "3", stdout: `{"data":{}}`, want: "no child count for 3"},
		{name: "null_count", container: "3", stdout: children("null", `[]`), want: "no child count for 3"},
		{name: "count_not_a_number", container: "3", stdout: children(`"x"`, `[]`), want: "cannot read the answer of `gh api graphql`"},
		{name: "no_nodes", container: "3", stdout: `{"data":{"repository":{"issue":{"subIssues":{"totalCount":1}}}}}`, want: "cannot read the children of 3"},
		{name: "nodes_not_a_list", container: "3", stdout: children("1", `{"number":1}`), want: "cannot read the answer"},
		{name: "child_without_number", container: "3", stdout: children("1", `[{"state":"OPEN"}]`), want: "no readable number"},
		{name: "gh_fails", container: "3", fail: true, want: "cannot read the children of 3"},
		{name: "container_not_a_number", container: "3; drop", want: "cannot read the children of 3; drop"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gh, run := newGithub(t)
			if tt.fail {
				run.planFail("boom", "api", "graphql")
			} else {
				run.plan(tt.stdout, "api", "graphql")
			}

			_, err := gh.Children(context.Background(), tt.container)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, store.CodeRefused, codeOf(t, err))
		})
	}
}

func TestGithubShow_PrintsTitleBodyAndEveryComment(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.plan(`{"number":5,"title":"Parser","body":"It drops rows.","comments":[`+
		`{"author":{"login":"ann"},"createdAt":"2026-01-02T03:04:05Z","body":"first"},`+
		`{"body":"second"}]}`, "issue", "view")

	got, err := gh.Show(context.Background(), "5")
	require.NoError(t, err)
	assert.Equal(t, "# #5 Parser\n\nIt drops rows.\n\n"+
		"## comment by ann at 2026-01-02T03:04:05Z\n\nfirst\n\n## comment by null at null\n\nsecond\n", got)
	assert.Equal(t, []string{"issue", "view", "5", "--repo", ghRepo, "--json", "number,title,body,comments"}, run.calls[0])
}

func TestGithubShow_NoBodyAndNoCommentsKeepsTheBlankLines(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.plan(`{"number":5,"title":"T","body":null,"comments":[]}`, "issue", "view")

	got, err := gh.Show(context.Background(), "5")
	require.NoError(t, err)
	assert.Equal(t, "# #5 T\n\n\n\n\n", got)
}

func TestGithubShow_RefusesWithCodeTwo(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"not_json":            "nope",
		"empty":               "",
		"no_comments_list":    `{"number":5,"title":"T","comments":null}`,
		"missing_comments":    `{"number":5,"title":"T"}`,
		"no_title":            `{"number":5,"comments":[]}`,
		"no_number":           `{"title":"T","comments":[]}`,
		"comments_not_a_list": `{"number":5,"title":"T","comments":{}}`,
	}

	for name, stdout := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gh, run := newGithub(t)
			run.plan(stdout, "issue", "view")

			_, err := gh.Show(context.Background(), "5")
			require.Error(t, err)
			assert.Equal(t, store.CodeRefused, codeOf(t, err))
		})
	}

	gh, run := newGithub(t)
	run.planFail("gone", "issue", "view")

	_, err := gh.Show(context.Background(), "5")
	require.EqualError(t, err, "REFUSE: cannot read item 5 in owner/name")
	assert.Equal(t, store.CodeRefused, codeOf(t, err))
}

func TestGithubClose_RecordsTheReasonAndAFailedCloseCarriesCodeOne(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.plan("", "issue", "close")
	require.NoError(t, gh.Close(context.Background(), "5", "done"))
	assert.Equal(t, []string{"issue", "close", "5", "--repo", ghRepo, "--comment", "done"}, run.calls[0])

	failing, bad := newGithub(t)
	bad.answers = append(bad.answers, answer{words: []string{"issue", "close"}, result: model.CommandResult{ExitCode: 1, Stderr: " nope \n"}})

	err := failing.Close(context.Background(), "5", "done")
	require.EqualError(t, err, "store: cannot close 5: nope")
	assert.Equal(t, store.CodeCloseFailed, codeOf(t, err))
}

func planCreate(t *testing.T, run *scripted, existing ...map[string]any) {
	t.Helper()

	run.plan(toJSON(t, append([]map[string]any{}, existing...)), "issue", "list")
	run.plan("https://github.com/owner/name/issues/42\n", "issue", "create")
}

func TestGithubCreate_FilesWithLabelsAndReturnsTheNumber(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	planCreate(t, run, issue(1, "other"))
	body := bodyFile(t)

	number, err := gh.Create(context.Background(), store.CreateRequest{Title: "New", BodyFile: body, Severity: model.SeverityP1})
	require.NoError(t, err)
	assert.Equal(t, "42", number)
	assert.Equal(t, []string{"issue", "create", "--repo", ghRepo, "--title", "New", "--body-file", body, "--label", "P1"}, run.calls[1])

	full, runFull := newGithub(t)
	planCreate(t, runFull)
	_, err = full.Create(context.Background(), store.CreateRequest{Title: "New", BodyFile: body, Severity: model.SeverityP0, Kind: "bug", Assignee: "ann"})
	require.NoError(t, err)
	assert.Equal(t, []string{"issue", "create", "--repo", ghRepo, "--title", "New", "--body-file", body, "--label", "P0", "--label", "bug", "--assignee", "ann"}, runFull.calls[1])
}

func TestGithubCreate_DuplicateTitles(t *testing.T) {
	t.Parallel()

	body := bodyFile(t)
	tests := []struct {
		name  string
		title string
		allow bool
		dup   bool
	}{
		{name: "same_title", title: "Same Title", dup: true},
		{name: "case_and_blanks_ignored", title: "  same \t title ", dup: true},
		{name: "more_than_case_and_blanks", title: "Same Titles", dup: false},
		{name: "allow_duplicate_files_anyway", title: "same title", allow: true, dup: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gh, run := newGithub(t)
			planCreate(t, run, issue(7, "same title"))

			_, err := gh.Create(context.Background(), store.CreateRequest{Title: tt.title, BodyFile: body, Severity: model.SeverityP2, AllowDuplicate: tt.allow})
			if !tt.dup {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, "REFUSED: 7 is open with the same title. Comment there, or pass --allow-duplicate.")
			assert.Equal(t, store.CodeDuplicate, codeOf(t, err))
			assert.False(t, run.called("issue", "create"))
		})
	}
}

func TestGithubCreate_RefusesWithoutFiling(t *testing.T) {
	t.Parallel()

	body := bodyFile(t)
	request := store.CreateRequest{Title: "New", BodyFile: body, Severity: model.SeverityP2}

	tests := []struct {
		name  string
		setup func(*scripted)
		want  string
	}{
		{name: "unreadable_open_issues", setup: func(r *scripted) { r.planFail("x", "issue", "list") }, want: "cannot list the open issues"},
		{name: "full_page", setup: func(r *scripted) { r.plan(toJSON(t, fill(1000, nil)), "issue", "list") }, want: "may be cut off"},
		{name: "gh_fails", setup: func(r *scripted) { r.plan("[]", "issue", "list"); r.planFail("x", "issue", "create") }, want: "gh issue create failed"},
		{name: "no_url", setup: func(r *scripted) { r.plan("[]", "issue", "list"); r.plan("created it\n", "issue", "create") }, want: "'created it', not an issue URL"},
		{name: "url_without_number", setup: func(r *scripted) { r.plan("[]", "issue", "list"); r.plan("https://x/issues/abc", "issue", "create") }, want: "not an issue URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gh, run := newGithub(t)
			tt.setup(run)

			_, err := gh.Create(context.Background(), request)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, store.CodeRefused, codeOf(t, err))
		})
	}

	_, err := gh0(t).Create(context.Background(), store.CreateRequest{})
	require.ErrorContains(t, err, "create needs --title")

	broken, run := newGithub(t)
	run.plan(`[{"number":1}]`, "issue", "list")

	_, err = broken.Create(context.Background(), request)
	require.ErrorContains(t, err, "no readable title")
}

func TestCreateRequest_Validate(t *testing.T) {
	t.Parallel()

	body := bodyFile(t)
	empty := t.TempDir() + "/empty.md"
	require.NoError(t, writeFile(empty, ""))

	tests := []struct {
		name    string
		request store.CreateRequest
		want    string
	}{
		{name: "no_title", request: store.CreateRequest{BodyFile: body, Severity: model.SeverityP1}, want: "create needs --title"},
		{name: "no_body_file", request: store.CreateRequest{Title: "t", Severity: model.SeverityP1}, want: "create needs --body-file"},
		{name: "missing_body_file", request: store.CreateRequest{Title: "t", BodyFile: body + ".gone", Severity: model.SeverityP1}, want: "create needs --body-file"},
		{name: "empty_body_file", request: store.CreateRequest{Title: "t", BodyFile: empty, Severity: model.SeverityP1}, want: "create needs --body-file"},
		{name: "no_severity", request: store.CreateRequest{Title: "t", BodyFile: body}, want: "got ''"},
		{name: "severity_p4", request: store.CreateRequest{Title: "t", BodyFile: body, Severity: "P4"}, want: "got 'P4'"},
		{name: "unknown_severity", request: store.CreateRequest{Title: "t", BodyFile: body, Severity: model.SeverityUnknown}, want: "got 'P?'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.request.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, store.CodeRefused, codeOf(t, err))
		})
	}

	for _, severity := range model.Severities() {
		require.NoError(t, store.CreateRequest{Title: "t", BodyFile: body, Severity: severity}.Validate())
	}
}

const linkAnswer = `{"data":{"repository":{"p":{"id":"PARENT"},"c":{"id":"CHILD"}}}}`

func TestGithubCreate_LinksTheParentBySubIssue(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	planCreate(t, run)
	run.planContaining(linkAnswer, "p:issue")
	run.planContaining("{}", "addSubIssue")

	number, err := gh.Create(context.Background(), store.CreateRequest{Title: "T", BodyFile: bodyFile(t), Severity: model.SeverityP2, Parent: "9"})
	require.NoError(t, err)
	assert.Equal(t, "42", number)

	last := run.calls[len(run.calls)-1][3]
	assert.Contains(t, last, `issueId:"PARENT",subIssueId:"CHILD"`)
	assert.Contains(t, run.calls[2][3], "p:issue(number:9){id} c:issue(number:42){id}")
}

func TestGithubCreate_AFailedLinkCarriesTheNumberAndCodeFour(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		parent string
		setup  func(*scripted)
	}{
		{name: "parent_not_a_number", parent: "abc", setup: func(*scripted) {}},
		{name: "id_lookup_fails", parent: "9", setup: func(r *scripted) {
			r.answers = append(r.answers, answer{contains: "p:issue", result: model.CommandResult{ExitCode: 1}})
		}},
		{name: "ids_unreadable", parent: "9", setup: func(r *scripted) { r.planContaining("nope", "p:issue") }},
		{name: "child_id_missing", parent: "9", setup: func(r *scripted) { r.planContaining(`{"data":{"repository":{"p":{"id":"P"}}}}`, "p:issue") }},
		{name: "parent_id_missing", parent: "9", setup: func(r *scripted) { r.planContaining(`{"data":{"repository":{"c":{"id":"C"}}}}`, "p:issue") }},
		{name: "mutation_fails", parent: "9", setup: func(r *scripted) {
			r.planContaining(linkAnswer, "p:issue")
			r.answers = append(r.answers, answer{contains: "addSubIssue", result: model.CommandResult{ExitCode: 1}})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gh, run := newGithub(t)
			planCreate(t, run)
			tt.setup(run)

			number, err := gh.Create(context.Background(), store.CreateRequest{Title: "T", BodyFile: bodyFile(t), Severity: model.SeverityP2, Parent: tt.parent})
			assert.Equal(t, "42", number)
			assert.Equal(t, store.CodeUnlinked, codeOf(t, err))

			var failure *store.Error
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, "42", failure.ItemID)
			assert.Contains(t, failure.Error(), "filed #42 but could not link it under #"+tt.parent)
		})
	}
}

func gh0(t *testing.T) *store.GithubStore {
	t.Helper()

	gh, _ := newGithub(t)

	return gh
}

func TestDecodeRefusals_KeepTheirCause(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.plan("null", "issue", "list")

	_, err := gh.OpenItems(context.Background())
	require.ErrorIs(t, err, model.ErrNullAnswer)

	for _, answer := range []string{"nope", "[] []"} {
		gh, run = newGithub(t)
		run.plan(answer, "issue", "list")

		_, err = gh.OpenItems(context.Background())

		var syntax *json.SyntaxError
		require.ErrorAs(t, err, &syntax, answer)
		require.ErrorContains(t, err, "gh issue list", answer)
	}
}
