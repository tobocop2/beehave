package main

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/claim"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
	"github.com/tobocop2/obee/internal/store/beads"
	"github.com/tobocop2/obee/internal/store/beads/beadstest"
)

// pullForge is a forge with the pull requests it lists by state. The methods it does not define panic, so a test shows every call the code makes.
type pullForge struct {
	forge.Forge

	mu      sync.Mutex
	listed  map[string][]model.PullRequest
	listErr error
	state   string
	drafts  []draftCall
	asked   [][]string
}

type draftCall struct {
	pr    int
	draft bool
}

func (f *pullForge) PRList(_ context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, args)

	for i, arg := range args {
		if arg == "--state" {
			return f.listed[args[i+1]], f.listErr
		}
	}

	return nil, f.listErr
}

func (f *pullForge) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	head, state := "abc", f.state

	return model.PullRequest{HeadRefOid: &head, State: &state}, nil
}

func (*pullForge) OpenChangeRequests(context.Context, int) ([]model.ChangeRequest, error) {
	return nil, nil
}

func (*pullForge) UnresolvedThreads(context.Context, int) ([]model.ReviewThread, error) {
	return nil, nil
}

func (f *pullForge) SetDraft(_ context.Context, pr int, draft bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.drafts = append(f.drafts, draftCall{pr, draft})

	return nil
}

func pullsOver(f *pullForge) pulls {
	return pulls{
		projects: map[string]config.Project{"p": {}},
		forge:    func(model.Project) (forge.Forge, error) { return f, nil },
	}
}

func TestPulls_PullReadsThePullRequestOnTheBranchOfTheItem(t *testing.T) {
	t.Parallel()

	f := &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 7}}}, state: "OPEN"}

	standing, err := pullsOver(f).Pull(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	assert.Equal(t, 7, standing.Number)
	assert.Equal(t, "abc", standing.Head)
	assert.Equal(t, []string{"--head", "bb-1", "--state", "open", "--json", "number"}, f.asked[0])
}

func TestPulls_PullFindsAMergedPullRequestWhenNoneIsOpen(t *testing.T) {
	t.Parallel()

	f := &pullForge{listed: map[string][]model.PullRequest{"merged": {{Number: 9}}}, state: "MERGED"}

	standing, err := pullsOver(f).Pull(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	assert.Equal(t, 9, standing.Number)
	assert.True(t, standing.Merged)
}

func TestPulls_AnItemWithoutOrWithManyPullRequestsIsAnError(t *testing.T) {
	t.Parallel()

	none := &pullForge{}
	_, err := pullsOver(none).Pull(t.Context(), "p", "bb-1")
	require.ErrorIs(t, err, errNoPull)

	many := &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 1}, {Number: 2}}}}
	_, err = pullsOver(many).Pull(t.Context(), "p", "bb-1")
	require.ErrorIs(t, err, errManyPull)

	broken := &pullForge{listErr: errFake}
	_, err = pullsOver(broken).Pull(t.Context(), "p", "bb-1")
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "find the pull request of item bb-1")
}

func TestPulls_AProjectTheConfigLacksIsAnErrorForEveryCall(t *testing.T) {
	t.Parallel()

	p := pullsOver(&pullForge{})
	ctx := t.Context()

	_, pullErr := p.Pull(ctx, "zzz", "bb-1")
	_, watchErr := p.Watch(ctx, "zzz", 1)

	for _, err := range []error{pullErr, watchErr, p.Ready(ctx, "zzz", "bb-1"), p.SetDraft(ctx, "zzz", 1, true)} {
		require.ErrorIs(t, err, errUnknownProject)
	}
}

func TestPulls_AForgeThatCannotBeBuiltIsAnError(t *testing.T) {
	t.Parallel()

	p := pulls{projects: map[string]config.Project{"p": {}}, forge: func(model.Project) (forge.Forge, error) { return nil, errFake }}

	require.ErrorIs(t, p.Ready(t.Context(), "p", "bb-1"), errFake)
}

func TestPulls_ReadyTakesThePullRequestOutOfDraft(t *testing.T) {
	t.Parallel()

	f := &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 7}}}}

	require.NoError(t, pullsOver(f).Ready(t.Context(), "p", "bb-1"))
	assert.Equal(t, []draftCall{{7, false}}, f.drafts)

	none := &pullForge{}
	require.ErrorIs(t, pullsOver(none).Ready(t.Context(), "p", "bb-1"), errNoPull)
}

func TestPulls_WatchReadsOnceAndSetDraftChangesThePullRequest(t *testing.T) {
	t.Parallel()

	f := &pullForge{state: "MERGED"}
	p := pullsOver(f)

	outcome, err := p.Watch(t.Context(), "p", 7)
	require.NoError(t, err)
	assert.Equal(t, review.Merged, outcome.Kind)

	require.NoError(t, p.SetDraft(t.Context(), "p", 7, true))
	assert.Equal(t, []draftCall{{7, true}}, f.drafts)
}

func leasesOver(t *testing.T, owner string) (leases, *beads.Store) {
	t.Helper()

	rows := beadsOver(t, beadstest.NewBd(t))

	return leases{
		projects: map[string]config.Project{"p": {}},
		rows:     func(context.Context, model.Project) (claimStore, error) { return claimStore{Rows: rows}, nil },
		owner:    func() string { return owner },
	}, rows
}

func TestLeases_TheLocalClaimStoreTakesRenewsAndReleasesAnItemAndEveryCallClosesTheStore(t *testing.T) {
	t.Parallel()

	table := newAdapters(nil, stateAt(t.TempDir()))
	project := localProjectNamed("p")
	l := leases{projects: map[string]config.Project{"p": {Project: project}}, rows: table.leases, owner: func() string { return "ownerone" }}

	owner, err := l.Claim(t.Context(), "p", "fix-login")
	require.NoError(t, err)

	text, err := l.Beat(t.Context(), "p", owner)
	require.NoError(t, err)
	assert.Empty(t, text)

	require.NoError(t, l.Release(t.Context(), "p", "fix-login", owner))

	l.owner = func() string { return "ownertwo" }
	_, err = l.Claim(t.Context(), "p", "fix-login")
	require.NoError(t, err, "the released item is free to take again")
}

type closingRows struct {
	claim.Rows

	closed int
	err    error
}

func (c *closingRows) Close() error {
	c.closed++

	return c.err
}

func TestLeases_ACallClosesTheStoreAndReportsAFailedClose(t *testing.T) {
	t.Parallel()

	l, rows := leasesOver(t, "ownerone")
	closing := &closingRows{Rows: rows, err: errFake}
	l.rows = func(context.Context, model.Project) (claimStore, error) {
		return claimStore{Rows: closing, closer: closing}, nil
	}

	_, err := l.Claim(t.Context(), "p", "12")

	require.ErrorIs(t, err, errFake)
	assert.Equal(t, 1, closing.closed)

	_, err = l.Beat(t.Context(), "p", "ownerone")
	require.ErrorIs(t, err, errFake)

	require.ErrorIs(t, l.Release(t.Context(), "p", "12", "ownerone"), errFake)
	assert.Equal(t, 3, closing.closed)
}

func TestPulls_AProjectWithNoForgeGetsTheTypedNoForgeErrorFromEveryCall(t *testing.T) {
	t.Parallel()

	p := pulls{projects: map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}, forge: newAdapters(nil, nil).forge}
	ctx := t.Context()

	_, pullErr := p.Pull(ctx, "p", "bb-1")
	_, watchErr := p.Watch(ctx, "p", 1)

	for _, err := range []error{pullErr, watchErr, p.Ready(ctx, "p", "bb-1"), p.SetDraft(ctx, "p", 1, true)} {
		require.ErrorIs(t, err, forge.ErrNoForge)
	}
}

func TestLeases_ClaimTakesTheItemAndReleaseGivesItBack(t *testing.T) {
	t.Parallel()

	l, rows := leasesOver(t, "ownerone")

	owner, err := l.Claim(t.Context(), "p", "12")
	require.NoError(t, err)
	assert.Equal(t, "ownerone", owner)
	assert.Equal(t, holderOf(t, "ownerone"), heldBy(t, rows, "12"))

	text, err := l.Beat(t.Context(), "p", owner)
	require.NoError(t, err)
	assert.Empty(t, text)

	require.NoError(t, l.Release(t.Context(), "p", "12", owner))
	assert.Empty(t, heldBy(t, rows, "12"))
}

func TestLeases_AClaimThatAnotherOwnerHoldsIsRefusedWithNoToken(t *testing.T) {
	t.Parallel()

	l, rows := leasesOver(t, "ownerone")
	_, err := l.Claim(t.Context(), "p", "12")
	require.NoError(t, err)

	l.owner = func() string { return "ownertwo" }

	owner, err := l.Claim(t.Context(), "p", "12")

	require.Error(t, err)
	assert.Empty(t, owner)
	assert.Equal(t, holderOf(t, "ownerone"), heldBy(t, rows, "12"))
}

func holderOf(t *testing.T, owner string) string {
	t.Helper()

	holder, err := claim.OwnerOf(owner)
	require.NoError(t, err)

	return holder
}

func TestLeases_AProjectTheConfigLacksIsAnErrorForEveryCall(t *testing.T) {
	t.Parallel()

	l, _ := leasesOver(t, "ownerone")
	_, claimErr := l.Claim(t.Context(), "zzz", "12")
	_, beatErr := l.Beat(t.Context(), "zzz", "ownerone")

	for _, err := range []error{claimErr, beatErr, l.Release(t.Context(), "zzz", "12", "ownerone")} {
		require.ErrorIs(t, err, errUnknownProject)
	}
}

func TestLeases_AClaimStoreThatCannotBeBuiltIsAnError(t *testing.T) {
	t.Parallel()

	l := leases{projects: map[string]config.Project{"p": {}}, rows: func(context.Context, model.Project) (claimStore, error) { return claimStore{}, errFake }, owner: ownerToken}

	_, err := l.Claim(t.Context(), "p", "12")

	require.ErrorIs(t, err, errFake)
}

func TestOwnerToken_IsAValidOwnerAndDiffersEachTime(t *testing.T) {
	t.Parallel()

	first, second := ownerToken(), ownerToken()

	_, err := claim.OwnerOf(first)
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
}

func TestNewLife_BuildsTheLifecycleOverTheTableOfTheConfig(t *testing.T) {
	t.Parallel()

	life := newLife(config.Config{Projects: map[string]config.Project{"p": {}}}, newAdapters(nil, nil))

	_, err := life.Pull(t.Context(), "zzz", "bb-1")
	require.ErrorIs(t, err, errUnknownProject)

	_, err = life.Claim(t.Context(), "zzz", "12")
	require.ErrorIs(t, err, errUnknownProject)
}
