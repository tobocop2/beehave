package main

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
	"github.com/tobocop2/beehave/internal/store/beads"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
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
	// refs and refsErr are the answer of ClosingRefs.
	refs    model.ClosingRefs
	refsErr error
	// posted is the comments on the pull request. assigned holds each AddAssignee call as number|login.
	posted     []model.Comment
	commentErr error
	assigned   []string
	// assignErr fails each AddAssignee call after the first assignOK calls.
	assignErr error
	assignOK  int
}

func (f *pullForge) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	return f.refs, f.refsErr
}

// Comments, PostComment and AddAssignee keep what the pulls type sent to the forge. commentErr and assignErr fail the matching write.
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

func (f *pullForge) AddAssignee(_ context.Context, number int, login string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.assigned = append(f.assigned, strconv.Itoa(number)+"|"+login)

	if f.assignErr != nil && len(f.assigned) > f.assignOK {
		return f.assignErr
	}

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

// treePull is a pullForge that reads the tree of a commit from trees. A commit that trees lacks cannot be read.
type treePull struct {
	*pullForge

	trees map[string]string
}

func (f treePull) CommitTree(_ context.Context, commit string) (string, error) {
	if tree, ok := f.trees[commit]; ok {
		return tree, nil
	}

	return "", errFake
}

func (treePull) TreeBlobs(context.Context, string) (map[string]string, error) { return nil, errFake }

func TestPulls_ReviewedLetsAReviewStandForAHeadWithTheSameTreeAndNotForAnotherTree(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		trees     map[string]string
		rewritten string
		reasons   int
	}{
		"the same tree":    {map[string]string{"abc": "t1", "old": "t1"}, "old", 0},
		"another tree":     {map[string]string{"abc": "t1", "old": "t2"}, "", 1},
		"a tree not read":  {map[string]string{"abc": "t1"}, "", 2},
		"the reviewed one": {map[string]string{}, "", 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 7}}}, state: "OPEN"}
			p := pulls{
				projects: map[string]config.Project{"p": {}},
				forge:    func(model.Project) (forge.Forge, error) { return treePull{base, test.trees}, nil },
			}
			reviewed := "old"

			if name == "the reviewed one" {
				reviewed = "abc"
			}

			standing, err := p.Reviewed(t.Context(), "p", "bb-1", reviewed)

			require.NoError(t, err)
			assert.Equal(t, test.rewritten, standing.Rewritten)
			assert.Len(t, standing.Reasons(reviewed), test.reasons)
		})
	}
}

func TestPulls_ReviewedOfAnUnknownProjectIsAnError(t *testing.T) {
	t.Parallel()

	_, err := pullsOver(&pullForge{}).Reviewed(t.Context(), "zzz", "bb-1", "old")

	require.ErrorIs(t, err, errUnknownProject)
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
		number, err := pullsOver(f).Comment(t.Context(), "p", "bb-1", "run:take", "taken")
		require.NoError(t, err)
		assert.Equal(t, 7, number)
	}

	assert.Equal(t, []model.Comment{{Body: "taken\n\nbeehave-key: run:take"}}, f.posted)

	f.commentErr = errFake
	_, err := pullsOver(f).Comment(t.Context(), "p", "bb-1", "run:other", "x")
	require.ErrorIs(t, err, errFake)
	assert.Len(t, f.posted, 1, "a failed read of the comments posts nothing")

	_, err = pullsOver(&pullForge{}).Comment(t.Context(), "p", "bb-1", "k", "x")
	require.ErrorIs(t, err, forge.ErrNoPull)
}

func TestPulls_AssignAssignsThePullRequestAndWithLinkedItsClosingIssues(t *testing.T) {
	t.Parallel()

	f := &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 7}}}, refs: model.ClosingRefs{Issues: []int{12, 13}}}

	number, issues, err := pullsOver(f).Assign(t.Context(), "p", "bb-1", "alice", false)
	require.NoError(t, err)
	assert.Equal(t, 7, number)
	assert.Empty(t, issues)
	assert.Equal(t, []string{"7|alice"}, f.assigned)

	f.assigned = nil
	_, issues, err = pullsOver(f).Assign(t.Context(), "p", "bb-1", "alice", true)
	require.NoError(t, err)
	assert.Equal(t, []int{12, 13}, issues)
	assert.Equal(t, []string{"7|alice", "12|alice", "13|alice"}, f.assigned)
}

func TestPulls_AssignRefusesWhenAnyReadOrWriteFails(t *testing.T) {
	t.Parallel()

	open := map[string][]model.PullRequest{"open": {{Number: 7}}}

	_, _, err := pullsOver(&pullForge{}).Assign(t.Context(), "p", "bb-1", "a", true)
	require.ErrorIs(t, err, forge.ErrNoPull)

	_, _, err = pullsOver(&pullForge{listed: open, assignErr: errFake}).Assign(t.Context(), "p", "bb-1", "a", false)
	require.ErrorIs(t, err, errFake)

	_, _, err = pullsOver(&pullForge{listed: open, refsErr: errFake}).Assign(t.Context(), "p", "bb-1", "a", true)
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the issues that pull request 7 closes")

	late := &pullForge{listed: open, refs: model.ClosingRefs{Issues: []int{12}}, assignErr: errFake, assignOK: 1}
	_, _, err = pullsOver(late).Assign(t.Context(), "p", "bb-1", "a", true)
	require.ErrorIs(t, err, errFake)
}

func TestPulls_ClosingIssuesGivesTheIssuesOfTheForgeAndAnItemWithNoPullRequestIsNotFound(t *testing.T) {
	t.Parallel()

	f := &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 7}}}, refs: model.ClosingRefs{Issues: []int{12}}}

	issues, found, err := pullsOver(f).ClosingIssues(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []int{12}, issues)

	_, found, err = pullsOver(&pullForge{}).ClosingIssues(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	assert.False(t, found)

	noForge := pulls{projects: map[string]config.Project{"p": {}}, forge: func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge }}

	_, found, err = noForge.ClosingIssues(t.Context(), "p", "bb-1")

	require.NoError(t, err)
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
	require.ErrorIs(t, pullsOver(none).Ready(t.Context(), "p", "bb-1"), forge.ErrNoPull)
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

	life := newLife(config.Config{Projects: map[string]config.Project{"p": {}}}, newAdapters(nil, nil, nil))

	_, err := life.Pull(t.Context(), "zzz", "bb-1")
	require.ErrorIs(t, err, errUnknownProject)

	_, err = life.Claim(t.Context(), "zzz", "12")
	require.ErrorIs(t, err, errUnknownProject)
}
