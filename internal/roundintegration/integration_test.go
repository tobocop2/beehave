package roundintegration_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/roundintegration"
)

const (
	round = "integration/s1"
	trunk = "integration/r1"
)

var errForge = errors.New("the forge failed")

// world is a forge that answers the lookup (found), the list of merged pull requests (merged) and the view of a given number (view), and records each write.
type world struct {
	found, merged          []model.PullRequest
	view                   model.PullRequest
	listed                 [][]string
	created                []model.NewPull
	bodies                 []string
	failView, failFind     error
	failMerged, failCreate error
	failBody               error
	nextPR                 int
	viewedWith             []string
}

func (w *world) PRView(_ context.Context, _ int, fields ...string) (model.PullRequest, error) {
	w.viewedWith = fields

	return w.view, w.failView
}

func (w *world) PRList(_ context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	w.listed = append(w.listed, args)

	if slices.Contains(args, "merged") {
		return w.merged, w.failMerged
	}

	return w.found, w.failFind
}

func (w *world) CreatePR(_ context.Context, req model.NewPull) (int, error) {
	w.created = append(w.created, req)

	return w.nextPR, w.failCreate
}

func (w *world) SetBody(_ context.Context, _ int, body string) error {
	w.bodies = append(w.bodies, body)

	return w.failBody
}

func text(s string) *string { return &s }

func pull(number int, state string) model.PullRequest {
	return model.PullRequest{Number: number, State: text(state)}
}

func item(number int, title, branch string) model.PullRequest {
	return model.PullRequest{Number: number, Title: title, HeadRefName: text(branch), State: text("MERGED")}
}

func request() roundintegration.Request {
	return roundintegration.Request{Base: round, Into: trunk, Label: "made-by-beehave"}
}

func TestRun_FindsTheOpenPullRequestBeforeAMergedOneAndWritesItsBody(t *testing.T) {
	t.Parallel()

	w := &world{found: []model.PullRequest{pull(4, "MERGED"), pull(5, "OPEN")}, merged: []model.PullRequest{item(7, "Fix it", "12")}}

	got, err := roundintegration.Run(t.Context(), w, request())

	require.NoError(t, err)
	assert.Equal(t, roundintegration.Result{PR: 5, State: "OPEN"}, got)
	require.Len(t, w.bodies, 1)
	assert.Contains(t, w.bodies[0], "| #7 | Fix it |")
	assert.Contains(t, w.bodies[0], "- Closes #12")
	assert.Empty(t, w.created)
}

func TestRun_ReportsAMergedPullRequestAndLeavesItsBodyAlone(t *testing.T) {
	t.Parallel()

	w := &world{found: []model.PullRequest{pull(4, "MERGED")}}

	got, err := roundintegration.Run(t.Context(), w, request())

	require.NoError(t, err)
	assert.Equal(t, roundintegration.Result{PR: 4, State: "MERGED"}, got)
	assert.Empty(t, w.bodies)
	assert.Len(t, w.listed, 1)
}

func TestRun_UsesTheGivenNumberAndSearchesForNoOther(t *testing.T) {
	t.Parallel()

	w := &world{view: pull(0, "OPEN")}
	req := request()
	req.PR = 9

	got, err := roundintegration.Run(t.Context(), w, req)

	require.NoError(t, err)
	assert.Equal(t, 9, got.PR)
	assert.Equal(t, []string{"number", "state", "body"}, w.viewedWith)
	assert.Len(t, w.listed, 1)
	assert.Contains(t, w.listed[0], "merged")
}

func TestRun_AGivenPullRequestWithNoStateIsUnknownAndKeepsItsBody(t *testing.T) {
	t.Parallel()

	w := &world{view: model.PullRequest{}}
	req := request()
	req.PR = 9

	got, err := roundintegration.Run(t.Context(), w, req)

	require.NoError(t, err)
	assert.Equal(t, roundintegration.Result{PR: 9, State: roundintegration.StateUnknown}, got)
	assert.Empty(t, w.bodies)
}

func TestRun_OpensTheDraftWithTheLabelAndTheMergedRowsWhenNoneExists(t *testing.T) {
	t.Parallel()

	w := &world{merged: []model.PullRequest{item(7, "Fix it", "x")}, nextPR: 21}

	got, err := roundintegration.Run(t.Context(), w, request())

	require.NoError(t, err)
	assert.Equal(t, roundintegration.Result{PR: 21, State: "OPEN"}, got)
	require.Len(t, w.created, 1)
	assert.Equal(t, model.NewPull{Head: round, Base: trunk, Title: "Integration round s1", Body: w.created[0].Body, Label: "made-by-beehave"}, w.created[0])
	assert.Contains(t, w.created[0].Body, "| #7 | Fix it |")
}

func TestRun_WritesNothingWhenTheBodyIsAlreadyTheOneTheMergedPullRequestsGive(t *testing.T) {
	t.Parallel()

	merged := []model.PullRequest{item(7, "Fix it", "x")}
	open := pull(5, "OPEN")
	open.Body = roundintegration.Body(round, trunk, merged)
	w := &world{found: []model.PullRequest{open}, merged: merged}

	_, err := roundintegration.Run(t.Context(), w, request())

	require.NoError(t, err)
	assert.Empty(t, w.bodies)
}

func TestRun_AFullListOfMergedPullRequestsIsARefusalAndWritesNothing(t *testing.T) {
	t.Parallel()

	full := make([]model.PullRequest, 100)
	w := &world{found: []model.PullRequest{pull(5, "OPEN")}, merged: full}

	_, err := roundintegration.Run(t.Context(), w, request())

	require.ErrorIs(t, err, roundintegration.ErrFullList)
	assert.Empty(t, w.bodies)
}

func TestRun_AFailedCallIsAnErrorAndWritesNothing(t *testing.T) {
	t.Parallel()

	open := []model.PullRequest{pull(5, "OPEN")}
	given := request()
	given.PR = 5

	cases := map[string]struct {
		w   *world
		req roundintegration.Request
	}{
		"the_lookup":      {&world{failFind: errForge}, request()},
		"the_view":        {&world{failView: errForge}, given},
		"the_merged_list": {&world{found: open, failMerged: errForge}, request()},
		"the_open_call":   {&world{failCreate: errForge}, request()},
		"the_body_write":  {&world{found: open, merged: []model.PullRequest{item(7, "t", "x")}, failBody: errForge}, request()},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := roundintegration.Run(t.Context(), c.w, c.req)

			require.ErrorIs(t, err, errForge)
		})
	}
}

func TestBody_ListsItemsApartFromSyncsAndNamesEachIssueOnce(t *testing.T) {
	t.Parallel()

	merged := []model.PullRequest{
		item(9, "Pipe | title", "12"),
		item(3, "First", "12"),
		item(4, "Sync", "sync/"+round+"/abc12345"),
		item(5, "Other round", "sync/integration/s10/abc"),
		item(6, "Ends in digits", "fix-34"),
		{Number: 8, Title: "No branch"},
	}

	body := roundintegration.Body(round, trunk, merged)

	assert.Contains(t, body, "Round s1 collects the item pull requests merged into integration/s1.")
	assert.Contains(t, body, "## Pull requests merged into this branch (5 pull requests)")
	assert.Less(t, strings.Index(body, "| #3 |"), strings.Index(body, "| #9 |"))
	assert.Contains(t, body, `| #9 | Pipe \| title |`)
	assert.Contains(t, body, "## Syncs with the trunk (1 pull request)\n\n#4")
	assert.Equal(t, 1, strings.Count(body, "- Closes #12"))
	assert.NotContains(t, body, "Closes #34")
}

func TestBody_ARoundWithNoItemsSaysSo(t *testing.T) {
	t.Parallel()

	body := roundintegration.Body(round, trunk, nil)

	assert.Contains(t, body, "No item pull request has merged into this branch yet.")
	assert.NotContains(t, body, "## Syncs")
	assert.NotContains(t, body, "## Issues")
}

func TestBody_ManySyncsAreNumberedOnOneLine(t *testing.T) {
	t.Parallel()

	merged := []model.PullRequest{item(4, "s", "sync/"+round+"/a"), item(6, "s", "sync/"+round+"/b")}

	assert.Contains(t, roundintegration.Body(round, trunk, merged), "## Syncs with the trunk (2 pull requests)\n\n#4, #6")
}
