package main

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store/beads"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
)

// pullForge is a forge with the pull requests it lists by state. The methods it does not define panic, so a test shows every call the code makes.
type pullForge struct {
	forge.Forge

	mu      sync.Mutex
	listed  map[string][]model.PullRequest
	listErr error
	// opened is the answer of OpenPulls, and openBases holds the base of each call.
	opened    []model.OpenPull
	openBases []string
	state     string
	drafts    []draftCall
	asked     [][]string
	// refs and refsErr are the answer of ClosingRefs.
	refs    model.ClosingRefs
	refsErr error
	// posted is the comments on the pull request.
	posted     []model.Comment
	commentErr error
	// ready makes PRView report a pull request that is out of draft. readyOnCall does so once SetDraft has run. A fake that sets neither reports no draft state.
	ready       bool
	readyOnCall bool
	// viewErr fails PRView.
	viewErr error
}

// OpenPulls answers with the open pull requests that the test set, and keeps the base of each call. listErr fails the read.
func (f *pullForge) OpenPulls(_ context.Context, base string) ([]model.OpenPull, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.openBases = append(f.openBases, base)

	return slices.Clone(f.opened), f.listErr
}

func (f *pullForge) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	return f.refs, f.refsErr
}

// Comments and PostComment keep what the pulls type sent to the forge. commentErr fails the read.
func (f *pullForge) Comments(context.Context, int) ([]model.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.posted), f.commentErr
}

func (f *pullForge) PostComment(_ context.Context, _ int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.posted = append(f.posted, model.Comment{Body: body})

	return nil
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
	f.mu.Lock()
	defer f.mu.Unlock()

	head, state := "abc", f.state
	view := model.PullRequest{HeadRefOid: &head, State: &state}

	if f.ready || (f.readyOnCall && len(f.drafts) > 0) {
		notDraft := false
		view.IsDraft = &notDraft
	}

	return view, f.viewErr
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
	require.ErrorIs(t, err, forge.ErrNoPull)

	many := &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 1}, {Number: 2}}}}
	_, err = pullsOver(many).Pull(t.Context(), "p", "bb-1")
	require.ErrorIs(t, err, errManyPull)

	broken := &pullForge{listErr: errFake}
	_, err = pullsOver(broken).Pull(t.Context(), "p", "bb-1")
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "find the pull request of item bb-1")
}

func TestPulls_CommentPostsOneCommentForOneKeyEvenWhenTheCallRunsTwice(t *testing.T) {
	t.Parallel()

	f := &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 7}}}}

	for range 2 {
		number, err := pullsOver(f).Comment(t.Context(), "p", "bb-1", 0, "run:take", "taken")
		require.NoError(t, err)
		assert.Equal(t, 7, number)
	}

	assert.Equal(t, []model.Comment{{Body: "taken\n\nbeehave-key: run:take"}}, f.posted)

	f.commentErr = errFake
	_, err := pullsOver(f).Comment(t.Context(), "p", "bb-1", 0, "run:other", "x")
	require.ErrorIs(t, err, errFake)
	assert.Len(t, f.posted, 1, "a failed read of the comments posts nothing")

	_, err = pullsOver(&pullForge{}).Comment(t.Context(), "p", "bb-1", 0, "k", "x")
	require.ErrorIs(t, err, forge.ErrNoPull)
}

func TestPulls_CommentWithAPullRequestNumberPostsThereWithNoLookupOfTheItem(t *testing.T) {
	t.Parallel()

	f := &pullForge{}

	number, err := pullsOver(f).Comment(t.Context(), "p", "round/main", 12, "run:survival", "lost")

	require.NoError(t, err)
	assert.Equal(t, 12, number, "the item has no pull request, and the call answers with the number that it was given")
	assert.Equal(t, []model.Comment{{Body: "lost\n\nbeehave-key: run:survival"}}, f.posted)

	_, err = pullsOver(f).Comment(t.Context(), "unknown", "round/main", 12, "run:survival", "lost")
	require.ErrorIs(t, err, errUnknownProject)
}

func TestPulls_ClosingIssuesGivesTheIssuesOfTheForgeAndAnItemWithNoPullRequestIsNotFound(t *testing.T) {
	t.Parallel()

	f := &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 7}}}, refs: model.ClosingRefs{Issues: []int{12}, Elsewhere: []model.IssueRef{"other-org/other-repo#5"}}}

	issues, found, err := pullsOver(f).ClosingIssues(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []model.IssueRef{"#12", "other-org/other-repo#5"}, issues, "an issue of another repository stays in the scope as a full reference")

	_, found, err = pullsOver(&pullForge{}).ClosingIssues(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	assert.False(t, found)

	noForge := pulls{projects: map[string]config.Project{"p": {}}, forge: func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge }}

	_, found, err = noForge.ClosingIssues(t.Context(), "p", "bb-1")

	require.ErrorIs(t, err, forge.ErrNoForge, "a project with no forge is an error the scope step reads as the scope check being off")
	assert.False(t, found)
}

func TestPulls_ClosingIssuesFailsClosedWhenTheForgeOrTheSearchFails(t *testing.T) {
	t.Parallel()

	f := &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 7}}}, refsErr: errFake}

	_, found, err := pullsOver(f).ClosingIssues(t.Context(), "p", "bb-1")

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the issues that pull request 7 closes")
	assert.False(t, found)

	_, _, err = pullsOver(&pullForge{listErr: errFake}).ClosingIssues(t.Context(), "p", "bb-1")
	require.ErrorIs(t, err, errFake)
}

func TestPulls_AProjectTheConfigLacksIsAnErrorForEveryCall(t *testing.T) {
	t.Parallel()

	p := pullsOver(&pullForge{})
	ctx := t.Context()

	_, pullErr := p.Pull(ctx, "zzz", "bb-1")

	for _, err := range []error{pullErr} {
		require.ErrorIs(t, err, errUnknownProject)
	}
}

func TestPulls_AForgeThatCannotBeBuiltIsAnError(t *testing.T) {
	t.Parallel()

	p := pulls{projects: map[string]config.Project{"p": {}}, forge: func(model.Project) (forge.Forge, error) { return nil, errFake }}

	_, err := p.Pull(t.Context(), "p", "bb-1")

	require.ErrorIs(t, err, errFake)
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

func TestLeases_OthersNamesEachOtherOwnerWithItsItems(t *testing.T) {
	t.Parallel()

	table := newAdapters(nil, stateAt(t.TempDir()), nil)
	project := localProjectNamed("p")
	first := leases{projects: map[string]config.Project{"p": {Project: project}}, rows: table.leases, owner: func() string { return "ownerone" }}
	second := leases{projects: first.projects, rows: table.leases, owner: func() string { return "ownertwo" }}

	one, err := first.Claim(t.Context(), "p", "fix-login")
	require.NoError(t, err)

	two, err := second.Claim(t.Context(), "p", "fix-logout")
	require.NoError(t, err)

	lines, err := first.Others(t.Context(), "p", one)

	require.NoError(t, err)
	assert.Equal(t, []string{"session-" + two + ": fix-logout"}, lines)

	_, err = first.Others(t.Context(), "zzz", one)
	require.ErrorIs(t, err, errUnknownProject)
}

func TestLeases_TheLocalClaimStoreTakesRenewsAndReleasesAnItemAndEveryCallClosesTheStore(t *testing.T) {
	t.Parallel()

	table := newAdapters(nil, stateAt(t.TempDir()), nil)
	project := localProjectNamed("p")
	l := leases{projects: map[string]config.Project{"p": {Project: project}}, rows: table.leases, owner: func() string { return "ownerone" }}

	owner, err := l.Claim(t.Context(), "p", "fix-login")
	require.NoError(t, err)

	text, err := l.Beat(t.Context(), "p", owner)
	require.NoError(t, err)
	assert.Empty(t, text)

	renewed, err := l.Renewed(t.Context(), "p", owner, "fix-login")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), renewed, time.Minute, "the renewal is the time of the take or the last beat")

	require.NoError(t, l.Release(t.Context(), "p", "fix-login", owner))

	renewed, err = l.Renewed(t.Context(), "p", owner, "fix-login")
	require.NoError(t, err)
	assert.True(t, renewed.IsZero(), "a released item has no renewal")

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

	_, err = l.Renewed(t.Context(), "p", "ownerone", "12")
	require.ErrorIs(t, err, errFake)
	assert.Equal(t, 4, closing.closed)
}

func TestPulls_AProjectWithNoForgeGetsTheTypedNoForgeErrorFromEveryCall(t *testing.T) {
	t.Parallel()

	p := pulls{projects: map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}, forge: newAdapters(nil, nil, nil).forge}
	ctx := t.Context()

	_, pullErr := p.Pull(ctx, "p", "bb-1")

	for _, err := range []error{pullErr} {
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

	life := newLife(config.Config{Projects: map[string]config.Project{"p": {}}}, newAdapters(nil, nil, nil), worktrees{})

	_, err := life.Pull(t.Context(), "zzz", "bb-1")
	require.ErrorIs(t, err, errUnknownProject)

	_, err = life.Claim(t.Context(), "zzz", "12")
	require.ErrorIs(t, err, errUnknownProject)
}
