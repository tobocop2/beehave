package github_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/forge/github"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

const (
	pushed      = "2026-03-01T10:00:00Z"
	earlier     = "2026-03-01T09:00:00Z"
	later       = "2026-03-01T11:00:00Z"
	ownMarker   = "search(query"
	changesMark = "{latestOpinionatedReviews"
	threadsMark = "{reviewThreads"
)

func login(name string) fields { return fields{"login": name} }

// batchedNode is the pull request 7 node of the batched answer, with every list on one page.
func batchedNode() fields {
	return fields{
		"number": 7, "isDraft": true, "baseRefName": "round", "title": "Add it",
		"commits": fields{"nodes": []fields{{"commit": fields{"committedDate": pushed}}}},
		"reviews": fields{"totalCount": 3, "nodes": []fields{
			{"author": login("alice"), "submittedAt": earlier},
			{"author": login("bob"), "submittedAt": nil},
			{"author": login("carol"), "submittedAt": later},
		}},
		"latestOpinionatedReviews": fields{"totalCount": 2, "nodes": []fields{
			{"author": login("bob"), "state": "CHANGES_REQUESTED", "commit": fields{"oid": "0123456789abcdef"}},
			{"author": login("alice"), "state": "APPROVED", "commit": fields{"oid": "fedcba"}},
		}},
		"reviewThreads": fields{"totalCount": 3, "nodes": []fields{{"isResolved": true}, {"isResolved": false}, {"isResolved": false}}},
	}
}

func searchAnswer(t *testing.T, count int, nodes ...fields) string {
	t.Helper()

	return toJSON(t, fields{"data": fields{"search": fields{"issueCount": count, "nodes": nodes}}})
}

// perPullRun serves the same data as batchedNode through the per-pull-request reads.
func perPullRun(t *testing.T) *proctest.Scripted {
	t.Helper()

	run := &proctest.Scripted{}
	run.Plan(toJSON(t, fields{"commits": []fields{{"committedDate": earlier}, {"committedDate": pushed}}}), "pr", "view", "7")
	run.Plan(toJSON(t, []any{[]fields{
		{"user": login("alice"), "state": "APPROVED", "submitted_at": earlier},
		{"user": login("bob"), "state": "PENDING", "submitted_at": nil},
		{"user": login("carol"), "state": "COMMENTED", "submitted_at": later},
	}}), "api", "--paginate", "--slurp", "repos/owner/name/pulls/7/reviews")
	run.PlanContaining(graph(t, fields{"latestOpinionatedReviews": fields{"totalCount": 2, "nodes": []fields{
		{"author": login("bob"), "state": "CHANGES_REQUESTED", "commit": fields{"oid": "0123456789abcdef"}},
		{"author": login("alice"), "state": "APPROVED", "commit": fields{"oid": "fedcba"}},
	}}}), changesMark)
	run.PlanContaining(graph(t, fields{"reviewThreads": fields{"totalCount": 3, "nodes": []fields{
		{"isResolved": true}, {"isResolved": false}, {"isResolved": false},
	}}}), threadsMark)

	return run
}

// perPullRows builds the state of pull request 7 from the per-pull-request reads, as the reads alone give it.
func perPullRows(t *testing.T, f *github.Forge) model.OwnPull {
	t.Helper()

	ctx := context.Background()
	view, err := f.PRView(ctx, 7, "commits")
	require.NoError(t, err)

	reviews, err := f.Reviews(ctx, 7)
	require.NoError(t, err)

	changes, err := f.OpenChangeRequests(ctx, 7)
	require.NoError(t, err)

	threads, err := f.UnresolvedThreads(ctx, 7)
	require.NoError(t, err)

	pull := model.OwnPull{
		Number: 7, Draft: true, Base: "round", Title: "Add it", Push: view.Commits[len(view.Commits)-1].CommittedDate,
		Reviews: []model.ReviewStamp{}, ChangeRequests: changes, ChangesKnown: true, OpenThreads: len(threads), ThreadsKnown: true,
	}

	for _, review := range reviews {
		if review.SubmittedAt != nil {
			pull.Reviews = append(pull.Reviews, model.ReviewStamp{Login: review.User.Login, SubmittedAt: *review.SubmittedAt})
		}
	}

	return pull
}

func TestOwnPulls_TheBatchedReadAndThePerPullRequestReadsGiveTheSameState(t *testing.T) {
	t.Parallel()

	batched, run := fixed(t, searchAnswer(t, 1, batchedNode()))
	got, err := batched.OwnPulls(context.Background(), "me")
	require.NoError(t, err)
	require.Len(t, got, 1)

	want := perPullRows(t, newForge(t, repo, perPullRun(t)))
	assert.Equal(t, want, got[0])
	assert.Len(t, run.Calls, 1)
	assert.Contains(t, run.Calls[0], "q=repo:owner/name is:pr is:open author:me")
}

func TestOwnPulls_LongListsAreReadAgainOneAtATimeAndGiveTheSameState(t *testing.T) {
	t.Parallel()

	long := batchedNode()
	long["reviews"] = fields{"totalCount": 101, "nodes": []fields{}}
	long["latestOpinionatedReviews"] = fields{"totalCount": 101, "nodes": []fields{}}
	long["reviewThreads"] = fields{"totalCount": 101, "nodes": []fields{}}

	run := perPullRun(t)
	run.PlanContaining(searchAnswer(t, 1, long), ownMarker)
	f := newForge(t, repo, run)

	got, err := f.OwnPulls(context.Background(), "me")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, perPullRows(t, f), got[0])
}

func TestOwnPulls_ALongListThatThePerPullRequestReadRefusesIsUnknown(t *testing.T) {
	t.Parallel()

	long := batchedNode()
	long["latestOpinionatedReviews"] = fields{"totalCount": 101, "nodes": []fields{}}
	long["reviewThreads"] = fields{"totalCount": 101, "nodes": []fields{}}

	run := &proctest.Scripted{}
	run.PlanContaining(searchAnswer(t, 1, long), ownMarker)
	run.PlanContaining(graph(t, fields{"latestOpinionatedReviews": fields{"totalCount": 101, "nodes": []fields{{"author": login("a"), "state": "APPROVED"}}}}), changesMark)
	run.PlanContaining(graph(t, fields{"reviewThreads": fields{"totalCount": 101, "nodes": []fields{{"isResolved": true}}}}), threadsMark)

	got, err := newForge(t, repo, run).OwnPulls(context.Background(), "me")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.False(t, got[0].ChangesKnown)
	assert.False(t, got[0].ThreadsKnown)
	assert.Empty(t, got[0].ChangeRequests)
	assert.Zero(t, got[0].OpenThreads)
}

func TestOwnPulls_AFailedReadAgainStopsTheRead(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		key  string
		plan func(*proctest.Scripted)
	}{
		"reviews": {"reviews", func(r *proctest.Scripted) { r.PlanFail("boom", "api", "--paginate") }},
		"changes": {"latestOpinionatedReviews", func(r *proctest.Scripted) { r.PlanContainingFail(changesMark) }},
		"threads": {"reviewThreads", func(r *proctest.Scripted) { r.PlanContainingFail(threadsMark) }},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			long := batchedNode()
			long[c.key] = fields{"totalCount": 101, "nodes": []fields{}}

			run := &proctest.Scripted{}
			run.PlanContaining(searchAnswer(t, 1, long), ownMarker)
			c.plan(run)

			_, err := newForge(t, repo, run).OwnPulls(context.Background(), "me")
			require.Error(t, err)
		})
	}
}

func TestOwnPulls_ANullListIsUnknownAndAPullRequestWithNoCommitHasNoPush(t *testing.T) {
	t.Parallel()

	node := batchedNode()
	node["latestOpinionatedReviews"] = nil
	node["reviewThreads"] = nil
	node["commits"] = fields{"nodes": []fields{}}

	f, _ := fixed(t, searchAnswer(t, 1, node))
	got, err := f.OwnPulls(context.Background(), "me")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.False(t, got[0].ChangesKnown)
	assert.False(t, got[0].ThreadsKnown)
	assert.Empty(t, got[0].Push)
}

func TestOwnPulls_RefuseAFailedMalformedEmptyOrLongAnswer(t *testing.T) {
	t.Parallel()

	failed, _ := failing(t, "boom")
	_, err := failed.OwnPulls(context.Background(), "me")
	require.ErrorContains(t, err, "cannot read our open pull requests")

	bad, _ := fixed(t, "not json")
	_, err = bad.OwnPulls(context.Background(), "me")
	require.ErrorContains(t, err, "cannot read our open pull requests")

	for _, body := range []string{`{"data":null}`, `{"data":{"search":null}}`} {
		none, _ := fixed(t, body)
		_, err = none.OwnPulls(context.Background(), "me")
		require.ErrorContains(t, err, "the answer holds none")
	}
}

func TestOwnPulls_ACountOneAboveTheNodesRefusesTheAnswer(t *testing.T) {
	t.Parallel()

	one, _ := fixed(t, searchAnswer(t, 2, batchedNode()))
	_, err := one.OwnPulls(context.Background(), "me")
	require.ErrorContains(t, err, "2 pull requests")
}

// pagedAnswer is one page of the search: the count of all rows, the cursor of the next page ("" for the last) and the numbers on the page.
func pagedAnswer(t *testing.T, count int, next string, numbers ...int) string {
	t.Helper()

	nodes := make([]fields, 0, len(numbers))
	for _, n := range numbers {
		nodes = append(nodes, fields{"number": n})
	}

	return toJSON(t, fields{"data": fields{"search": fields{
		"issueCount": count, "nodes": nodes,
		"pageInfo": fields{"hasNextPage": next != "", "endCursor": next},
	}}})
}

func TestOwnPulls_ReadsEveryPageWithTheCursorAndSortsByNumber(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.PlanContaining(pagedAnswer(t, 3, "c1", 9, 4), ownMarker)
	run.PlanContaining(pagedAnswer(t, 3, "", 2), "after=c1")

	got, err := newForge(t, repo, run).OwnPulls(context.Background(), "me")
	require.NoError(t, err)

	numbers := make([]int, 0, len(got))
	for _, pull := range got {
		numbers = append(numbers, pull.Number)
	}

	assert.Equal(t, []int{2, 4, 9}, numbers)
	assert.Len(t, run.Calls, 2)
	assert.NotContains(t, run.Calls[0], "after=c1")
	assert.Contains(t, run.Calls[1], "after=c1")
}

func TestOwnPulls_ReadsUpToAThousandRowsAndRefusesMore(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.PlanContaining(pagedAnswer(t, 1000, "c1", rowsFrom(1)...), ownMarker)

	for page := 1; page < 10; page++ {
		next := "c" + strconv.Itoa(page+1)
		if page == 9 {
			next = ""
		}

		run.PlanContaining(pagedAnswer(t, 1000, next, rowsFrom(page*100+1)...), "after=c"+strconv.Itoa(page))
	}

	got, err := newForge(t, repo, run).OwnPulls(context.Background(), "me")
	require.NoError(t, err)
	assert.Len(t, got, 1000)
	assert.Len(t, run.Calls, 10)

	over, _ := fixed(t, pagedAnswer(t, 1001, "", 1))
	_, err = over.OwnPulls(context.Background(), "me")
	require.ErrorContains(t, err, "1001 pull requests, more than 1000")
}

func TestOwnPulls_RefuseAPageThatPromisesAnotherAndGivesNoCursor(t *testing.T) {
	t.Parallel()

	body := toJSON(t, fields{"data": fields{"search": fields{
		"issueCount": 2, "nodes": []fields{{"number": 1}}, "pageInfo": fields{"hasNextPage": true, "endCursor": ""},
	}}})
	f, _ := fixed(t, body)
	_, err := f.OwnPulls(context.Background(), "me")
	require.ErrorContains(t, err, "no cursor")
}

func TestOwnPulls_RefuseAForgeThatRepeatsAPageInsteadOfReadingForever(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"more rows than counted":       pagedAnswer(t, 2, "c1", 1),
		"no rows and another promised": pagedAnswer(t, 2, "c1"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f, _ := fixed(t, body)
			_, err := f.OwnPulls(context.Background(), "me")
			require.ErrorContains(t, err, "cannot read our open pull requests")
		})
	}
}

func rowsFrom(first int) []int {
	rows := make([]int, 0, 100)
	for n := first; n < first+100; n++ {
		rows = append(rows, n)
	}

	return rows
}

func TestTitle_ReadsTheTitleOfAnIssue(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "A title\n")
	title, err := f.Title(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, "A title", title)
	assert.True(t, run.Called("issue", "view", "5"))

	failed, _ := failing(t, "boom")
	_, err = failed.Title(context.Background(), 5)
	require.ErrorContains(t, err, "cannot read the title of #5")
}

func TestCheckNotStarted_ReadsTheFirstAnnotationOfTheRun(t *testing.T) {
	t.Parallel()

	const link = "https://github.com/owner/name/runs/42"

	cases := map[string]struct {
		body string
		want bool
	}{
		"the run was not started": {`[{"message":"The job was not started because of billing"}]`, true},
		"another message":         {`[{"message":"lint failed"}]`, false},
		"a null message":          {`[{"message":null}]`, false},
		"no annotation":           {`[]`, false},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f, run := fixed(t, c.body)
			got, err := f.CheckNotStarted(context.Background(), link)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
			assert.True(t, run.Called("api", "repos/owner/name/check-runs/42/annotations"))
		})
	}
}

func TestCheckNotStarted_RefuseARunIdThatIsNotAllDigits(t *testing.T) {
	t.Parallel()

	for _, link := range []string{"https://github.com/owner/name/runs/", "x/abc", "x/4a2", "x/-1", "", "42x"} {
		f, run := fixed(t, `[]`)
		_, err := f.CheckNotStarted(context.Background(), link)
		require.ErrorContains(t, err, "does not end in a check run number", link)
		assert.Empty(t, run.Calls, link)
	}
}

func TestCheckNotStarted_RefuseAFailedOrMalformedRead(t *testing.T) {
	t.Parallel()

	failed, _ := failing(t, "boom")
	_, err := failed.CheckNotStarted(context.Background(), "x/42")
	require.ErrorContains(t, err, "cannot read the annotations of check run 42")

	bad, _ := fixed(t, "not json")
	_, err = bad.CheckNotStarted(context.Background(), "x/42")
	require.ErrorContains(t, err, "cannot read the annotations of check run 42")
}
