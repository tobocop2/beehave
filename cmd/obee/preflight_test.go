package main

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/preflight"
)

// preflightForge answers the pull request read of a preflight with a full row.
type preflightForge struct {
	pullForge

	reads []int
}

func (f *preflightForge) PRView(_ context.Context, pr int, _ ...string) (model.PullRequest, error) {
	f.reads = append(f.reads, pr)

	return model.Decode[model.PullRequest]("test", `{"number":`+strconv.Itoa(pr)+`,"state":"`+f.state+`","isDraft":false,"headRefName":"bb-1","baseRefName":"integration","headRefOid":"abc","labels":[]}`)
}

// over is the pulls of project p over the forge.
func (f *preflightForge) over() pulls {
	return pulls{
		projects: map[string]config.Project{"p": {}},
		forge:    func(model.Project) (forge.Forge, error) { return f, nil },
	}
}

func (*preflightForge) IssueState(context.Context, int) (string, error) {
	return "", errFake
}

func (*preflightForge) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	return model.ClosingRefs{}, errFake
}

func (*preflightForge) BranchRef(context.Context, string) (string, bool, error) {
	return "abc", true, nil
}

func TestPulls_PreflightReadsAClosedPullRequestAsAStop(t *testing.T) {
	t.Parallel()

	f := &preflightForge{pullForge: pullForge{listed: map[string][]model.PullRequest{"closed": {{Number: 4}}}, state: "CLOSED"}}

	result, err := f.over().Preflight(t.Context(), "p", "bb-1", preflight.Request{}, "hold")

	require.NoError(t, err)
	assert.Equal(t, preflight.Closed, result.Stop)
	assert.Equal(t, []int{4}, f.reads)
}

func TestPulls_PreflightOfAnItemWithManyClosedPullRequestsReadsTheNewestOne(t *testing.T) {
	t.Parallel()

	f := &preflightForge{pullForge: pullForge{listed: map[string][]model.PullRequest{"closed": {{Number: 9}, {Number: 4}}}, state: "CLOSED"}}

	result, err := f.over().Preflight(t.Context(), "p", "bb-1", preflight.Request{}, "hold")

	require.NoError(t, err)
	assert.Equal(t, preflight.Closed, result.Stop)
	assert.Equal(t, []int{9}, f.reads, "the forge lists the newest first")
}

func TestPulls_PreflightOfAnItemWithManyOpenPullRequestsStillFails(t *testing.T) {
	t.Parallel()

	f := &preflightForge{pullForge: pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 9}, {Number: 4}}}, state: "OPEN"}}

	_, err := f.over().Preflight(t.Context(), "p", "bb-1", preflight.Request{}, "hold")

	require.ErrorIs(t, err, errManyPull)
}

func TestPulls_PreflightPrefersTheOpenPullRequestOverAnOlderClosedOne(t *testing.T) {
	t.Parallel()

	f := &preflightForge{pullForge: pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 8}}, "closed": {{Number: 4}}}, state: "OPEN"}}

	result, err := f.over().Preflight(t.Context(), "p", "bb-1", preflight.Request{ExpectBase: "integration", ExpectHead: "abc"}, "hold")

	require.NoError(t, err)
	assert.Equal(t, []int{8}, f.reads)
	assert.Equal(t, preflight.Refused, result.Stop, "a short head is refused, so the expectation reached the check")
}

func TestPulls_PreflightOfAnItemWithNoPullRequestFindsNothingChanged(t *testing.T) {
	t.Parallel()

	f := &preflightForge{}

	result, err := f.over().Preflight(t.Context(), "p", "bb-1", preflight.Request{}, "hold")

	require.NoError(t, err)
	assert.True(t, result.OK())
	assert.Empty(t, f.reads)
}

// closedListFails lets the open and merged lists pass and fails the closed one.
type closedListFails struct {
	preflightForge
}

func (f *closedListFails) PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error) {
	for i, arg := range args {
		if arg == "--state" && args[i+1] == "closed" {
			return nil, errFake
		}
	}

	return f.preflightForge.PRList(ctx, limit, args...)
}

func TestPulls_PreflightFailsWhenTheClosedListFailsAfterTheOthersFindNothing(t *testing.T) {
	t.Parallel()

	f := &closedListFails{}
	over := pulls{
		projects: map[string]config.Project{"p": {}},
		forge:    func(model.Project) (forge.Forge, error) { return f, nil },
	}

	_, err := over.Preflight(t.Context(), "p", "bb-1", preflight.Request{}, "")

	require.ErrorIs(t, err, errFake)
	assert.Contains(t, err.Error(), "find the pull request of item bb-1")
}

func TestPulls_PreflightFailsWhenTheListFailsOrTheProjectIsUnknown(t *testing.T) {
	t.Parallel()

	failing := &preflightForge{pullForge: pullForge{listErr: errFake}}
	_, err := failing.over().Preflight(t.Context(), "p", "bb-1", preflight.Request{}, "")
	require.ErrorIs(t, err, errFake)

	_, err = (&preflightForge{}).over().Preflight(t.Context(), "zzz", "bb-1", preflight.Request{}, "")
	require.ErrorIs(t, err, errUnknownProject)
}

// searchForge answers the search for a pull request that names the item, apart from the lists by branch.
type searchForge struct {
	preflightForge

	found    []model.PullRequest
	searched [][]string
}

func (f *searchForge) PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error) {
	if slices.Contains(args, "--search") {
		f.searched = append(f.searched, args)

		return f.found, nil
	}

	return f.preflightForge.PRList(ctx, limit, args...)
}

func (f *searchForge) run(t *testing.T, item string) preflight.Result {
	t.Helper()

	over := pulls{
		projects: map[string]config.Project{"p": {}},
		forge:    func(model.Project) (forge.Forge, error) { return f, nil },
	}

	result, err := over.Preflight(t.Context(), "p", item, preflight.Request{Own: []string{"me"}}, "")
	require.NoError(t, err)

	return result
}

func TestPulls_PreflightFindsAnOpenPullRequestThatNamesTheItemOnAnotherBranch(t *testing.T) {
	t.Parallel()

	f := &searchForge{found: []model.PullRequest{{Number: 31, Title: "Fix the parser", Body: "Closes #7."}}}
	f.state = "OPEN"

	result := f.run(t, "7")

	assert.Equal(t, preflight.Claimed, result.Stop, "a pull request on a branch that is not the item still claims the item")
	assert.Equal(t, []int{31}, f.reads)
	assert.Contains(t, f.searched[0], "open")
}

func TestPulls_PreflightFindsAPullRequestThatNamesAnItemIdInItsTitle(t *testing.T) {
	t.Parallel()

	f := &searchForge{found: []model.PullRequest{{Number: 32, Title: "bb-1: fix the parser"}}}
	f.state = "OPEN"

	assert.Equal(t, preflight.Claimed, f.run(t, "bb-1").Stop)
}

func TestPulls_PreflightIgnoresAPullRequestThatNamesAnotherItemWithTheSameDigits(t *testing.T) {
	t.Parallel()

	f := &searchForge{found: []model.PullRequest{
		{Number: 33, Body: "Closes #70"},
		{Number: 34, Body: "see #7a and bb-17"},
		{Number: 35, Title: "version 7"},
	}}
	f.state = "OPEN"

	result := f.run(t, "7")

	assert.True(t, result.OK(), "#70, #7a and a bare 7 are not the item")
	assert.Empty(t, f.reads)
}

func TestPulls_PreflightSearchesForANamedPullRequestOnlyWhenTheBranchFindsNone(t *testing.T) {
	t.Parallel()

	f := &searchForge{found: []model.PullRequest{{Number: 31, Body: "Closes #7"}}}
	f.state = "OPEN"
	f.listed = map[string][]model.PullRequest{"open": {{Number: 8}}}

	f.run(t, "7")

	assert.Equal(t, []int{8}, f.reads)
	assert.Empty(t, f.searched)
}

// searchFails fails the search for a pull request that names the item.
type searchFails struct {
	preflightForge
}

func (f *searchFails) PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error) {
	if slices.Contains(args, "--search") {
		return nil, errFake
	}

	return f.preflightForge.PRList(ctx, limit, args...)
}

func TestPulls_PreflightFailsWhenTheSearchFails(t *testing.T) {
	t.Parallel()

	f := &searchFails{}
	over := pulls{
		projects: map[string]config.Project{"p": {}},
		forge:    func(model.Project) (forge.Forge, error) { return f, nil },
	}

	_, err := over.Preflight(t.Context(), "p", "7", preflight.Request{}, "")

	require.ErrorIs(t, err, errFake)
	assert.Contains(t, err.Error(), "search the pull requests that name item 7")
}

func TestPulls_PreflightFindsAnOpenPullRequestThatNamesTheItemBehindAClosedOneOnTheBranch(t *testing.T) {
	t.Parallel()

	f := &searchForge{found: []model.PullRequest{{Number: 31, Body: "Closes #7"}}}
	f.state = "OPEN"
	f.listed = map[string][]model.PullRequest{"closed": {{Number: 5}}}

	result := f.run(t, "7")

	assert.Equal(t, preflight.Claimed, result.Stop, "an old closed pull request on the branch does not hide a live one that names the item")
	assert.Equal(t, []int{31}, f.reads)
}

func TestPulls_PreflightFailsWhenTheBranchHasManyMergedPullRequests(t *testing.T) {
	t.Parallel()

	f := &searchForge{}
	f.state = "OPEN"
	f.listed = map[string][]model.PullRequest{"merged": {{Number: 4}, {Number: 5}}}

	_, err := f.over().Preflight(t.Context(), "p", "7", preflight.Request{}, "")

	require.ErrorIs(t, err, errManyPull)
}

func TestPulls_PreflightFindsAnOpenPullRequestThatNamesTheItemBehindAMergedOneOnTheBranch(t *testing.T) {
	t.Parallel()

	f := &searchForge{found: []model.PullRequest{{Number: 31, Body: "Closes #7"}}}
	f.state = "OPEN"
	f.listed = map[string][]model.PullRequest{"merged": {{Number: 4}}}

	assert.Equal(t, preflight.Claimed, f.run(t, "7").Stop)
}
