package main

import (
	"context"
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
