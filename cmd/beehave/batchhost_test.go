package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/review"
)

// hostTrees gives the one folder that the host tests use, or an error.
type hostTrees struct {
	dir string
	err error
}

func (t hostTrees) Path(context.Context, string, string) (string, error) { return t.dir, t.err }

func (t hostTrees) Ensure(context.Context, string, string, string, string) (string, error) {
	return t.dir, t.err
}

// hostFetch is the fetch of the base in the host tests. The repository of the tests already holds the base, so it fetches nothing.
type hostFetch struct{ err error }

func (f hostFetch) Fetch(context.Context, string) error { return f.err }

// hostForge is the forge of the host tests: the tip of the base, and one pull request in a given state.
type hostForge struct {
	forge.Forge

	tip      string
	tipErr   error
	found    bool
	state    string
	viewErr  error
	listed   []model.PullRequest
	listErr  error
	viewHead string
	// pullReads counts the typed reads when it is set. readNumber is the number the typed read answers with, in place of the number asked for, when it is not zero.
	pullReads  *int
	readNumber int
	// rawRead is the JSON of the view that the typed read decodes and reads as the forge reads it, with its refusals, in place of the view of the fake.
	rawRead string
	// draft is the isDraft field of the view, nil when the forge sends none. closed is what the list of closed pull requests holds.
	draft  *bool
	closed []model.PullRequest
	// drafted records each SetDraft call as the number and the flag. setDraftErr is the error of that call.
	drafted     *[]string
	setDraftErr error
	// mergeable is the mergeable field of the view. changes and threads are the counts of change requests and unresolved threads, and changesErr and threadsErr fail those reads.
	mergeable              string
	changes, threads       int
	changesErr, threadsErr error
	// trees is the tree of each commit, and treeErr is the error of every tree read.
	trees   map[string]string
	treeErr error
}

func (f hostForge) SetDraft(_ context.Context, number int, draft bool) error {
	if f.drafted != nil {
		*f.drafted = append(*f.drafted, fmt.Sprintf("%d:%t", number, draft))
	}

	return f.setDraftErr
}

// pullFactsOf is the typed read that a fake serves from a view. It numbers the facts and refuses a view with no state, head or base, as the contract of the typed read says.
func pullFactsOf(number int, view model.PullRequest, err error) (model.PullFacts, error) {
	if err != nil {
		return model.PullFacts{}, err
	}

	facts := view.Facts()
	facts.Number = number

	if err := facts.Check(); err != nil {
		return model.PullFacts{}, err
	}

	return facts, nil
}

func (f hostForge) PullRead(ctx context.Context, number int) (model.PullFacts, error) {
	if f.pullReads != nil {
		*f.pullReads++
	}

	if f.rawRead != "" {
		view, err := model.Decode[model.PullRequest]("test", f.rawRead)
		if err != nil {
			return model.PullFacts{}, err
		}

		return view.Read(number)
	}

	view, err := f.PRView(ctx, number)
	facts, err := pullFactsOf(number, view, err)

	if f.readNumber != 0 {
		facts.Number = f.readNumber
	}

	return facts, err
}

func (f hostForge) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	return model.ClosingRefs{BaseBranch: "main", DefaultBranch: "main"}, nil
}

func (f hostForge) BranchRef(context.Context, string) (string, bool, error) {
	return f.tip, f.found, f.tipErr
}

func (f hostForge) PRList(_ context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	if slices.Contains(args, "closed") {
		return f.closed, f.listErr
	}

	return f.listed, f.listErr
}

func (f hostForge) PRView(_ context.Context, number int, _ ...string) (model.PullRequest, error) {
	if f.viewErr != nil {
		return model.PullRequest{}, f.viewErr
	}

	head, base := f.viewHead, "main"
	view := model.PullRequest{Number: number, HeadRefOid: &head, BaseRefName: &base, IsDraft: f.draft}

	if f.mergeable != "" {
		view.Mergeable = &f.mergeable
	}

	if f.state != "" { // a state that the forge did not send is nil, not an empty text
		view.State = &f.state
	}

	return view, nil
}

// hostGit runs git in dir and returns the trimmed output. The test fails when git fails.
func hostGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	identity := []string{"-c", "user.name=t", "-c", "user.email=t@example.com"}
	command := exec.CommandContext(t.Context(), "git", append(identity, args...)...) //nolint:gosec // a test runs git on its own temporary folder
	command.Dir = dir
	out, err := command.CombinedOutput()
	require.NoError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

// hostRepo makes a git repository with one commit and the remote origin set to itself, so that a fetch works with no network.
func hostRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	hostGit(t, dir, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o600))
	hostGit(t, dir, "add", "a.txt")
	hostGit(t, dir, "commit", "-q", "-m", "start")
	hostGit(t, dir, "remote", "add", "origin", ".")

	return dir
}

func hostOver(dir string, f hostForge, treeErr error) batchHost {
	return batchHost{
		pulls: pulls{
			projects: map[string]config.Project{"p": {Project: model.Project{Repo: "o/r"}}},
			forge:    func(model.Project) (forge.Forge, error) { return f, nil },
		},
		programs: programs{
			trees: hostTrees{dir: dir, err: treeErr},
			local: func(_, binary, folder string, timeout time.Duration) proc.Runner {
				return proc.ExecRunner{Binary: binary, Dir: folder, Timeout: timeout}
			},
		},
		base: hostFetch{},
	}
}

func TestBatchHost_TipReadsTheTipOnTheForgeAndTheTreeOfThatCommit(t *testing.T) {
	t.Parallel()

	dir := hostRepo(t)
	tip := hostGit(t, dir, "rev-parse", "HEAD")

	got, err := hostOver(dir, hostForge{tip: tip, found: true}, nil).Tip(t.Context(), "p", "7", "main")

	require.NoError(t, err)
	assert.Equal(t, tip, got.Commit)
	assert.Equal(t, hostGit(t, dir, "rev-parse", "HEAD^{tree}"), got.Tree)
}

func TestBatchHost_ProveLeavesAMemberUnprovenWhenTheForgeCannotCompareCommits(t *testing.T) {
	t.Parallel()

	landed := []batch.Member{{Item: "7", Head: "abc"}}

	got, err := hostOver(t.TempDir(), hostForge{}, nil).Prove(t.Context(), "p", "main", batch.Tip{}, batch.Tip{}, landed)

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "7", got[0].Item)
	assert.Contains(t, got[0].Reason, "unproven")

	_, err = hostOver(t.TempDir(), hostForge{}, nil).Prove(t.Context(), "nope", "main", batch.Tip{}, batch.Tip{}, landed)
	require.Error(t, err)
}

func TestBatchHost_TipRefusesABaseThatTheForgeCannotFind(t *testing.T) {
	t.Parallel()

	_, err := hostOver(hostRepo(t), hostForge{}, nil).Tip(t.Context(), "p", "7", "main")

	require.ErrorContains(t, err, "cannot read the tip of main on the forge of project p: the branch is not there")

	_, err = hostOver(hostRepo(t), hostForge{tipErr: errFake}, nil).Tip(t.Context(), "p", "7", "main")
	require.ErrorIs(t, err, errFake)
}

func TestBatchHost_TipFailsForAnUnknownProjectAndForAMissingFolder(t *testing.T) {
	t.Parallel()

	host := hostOver(hostRepo(t), hostForge{tip: "abc", found: true}, nil)
	_, err := host.Tip(t.Context(), "other", "7", "main")
	require.ErrorIs(t, err, errUnknownProject)

	_, err = hostOver("", hostForge{tip: "abc", found: true}, errFake).Tip(t.Context(), "p", "7", "main")
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "find the folder of item 7")
}

func TestBatchHost_TipFailsWhenTheFetchFailsAndWhenTheTipIsNotInTheRepository(t *testing.T) {
	t.Parallel()

	dir := hostRepo(t)
	host := hostOver(dir, hostForge{tip: "abc", found: true}, nil)
	host.base = hostFetch{err: errFake}

	_, err := host.Tip(t.Context(), "p", "7", "main")
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "fetch the base main of project p")

	_, err = hostOver(dir, hostForge{tip: strings.Repeat("e", 40), found: true}, nil).Tip(t.Context(), "p", "7", "main")
	require.ErrorContains(t, err, "git rev-parse of "+strings.Repeat("e", 40))
}

func TestBatchHost_CheckoutDetachesTheWorktreeAtTheCommitAndReturnsItsTree(t *testing.T) {
	t.Parallel()

	dir := hostRepo(t)
	first := hostGit(t, dir, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o600))
	hostGit(t, dir, "add", "b.txt")
	hostGit(t, dir, "commit", "-q", "-m", "second")

	tree, err := hostOver(dir, hostForge{}, nil).Checkout(t.Context(), "p", "7", first)

	require.NoError(t, err)
	assert.Equal(t, hostGit(t, dir, "rev-parse", first+"^{tree}"), tree)
	assert.Equal(t, first, hostGit(t, dir, "rev-parse", "HEAD"))
	assert.Empty(t, hostGit(t, dir, "branch", "--show-current"), "the worktree is detached, so no branch moves")
	assert.NoFileExists(t, filepath.Join(dir, "b.txt"))
}

func TestBatchHost_CheckoutFailsForACommitThatIsNotThereAndForAMissingFolder(t *testing.T) {
	t.Parallel()

	dir := hostRepo(t)
	missing := strings.Repeat("d", 40)

	_, err := hostOver(dir, hostForge{}, nil).Checkout(t.Context(), "p", "7", missing)
	require.ErrorContains(t, err, "git checkout of the union "+missing)

	_, err = hostOver("", hostForge{}, errFake).Checkout(t.Context(), "p", "7", missing)
	require.ErrorIs(t, err, errFake)
}

// TestBatchHost_GitMakesCommitsWithTheFixedIdentity changes the environment of the process, so it does not run in parallel.
func TestBatchHost_GitMakesCommitsWithTheFixedIdentity(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(name, "") // registers the restore of the old value
		require.NoError(t, os.Unsetenv(name))
	}

	dir := hostRepo(t)
	parent := hostGit(t, dir, "rev-parse", "HEAD")
	tree := hostGit(t, dir, "rev-parse", "HEAD^{tree}")

	git, err := hostOver(dir, hostForge{}, nil).Git(t.Context(), "p", "7")
	require.NoError(t, err)

	made, err := git.NewCommit(t.Context(), tree, parent)

	require.NoError(t, err)
	assert.Equal(t, "beehave beehave@localhost beehave beehave@localhost", hostGit(t, dir, "log", "-1", "--format=%an %ae %cn %ce", made))
	assert.Equal(t, parent, hostGit(t, dir, "rev-parse", made+"^"))
}

func TestBatchHost_GitFailsForAMissingFolder(t *testing.T) {
	t.Parallel()

	_, err := hostOver("", hostForge{}, errFake).Git(t.Context(), "p", "7")

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "find the folder of item 7")
}

func TestBatchHost_AnItemWithNoWorktreeIsRefusedAndGitNeverRunsInTheFolderOfTheProcess(t *testing.T) {
	t.Parallel()

	host := hostOver("", hostForge{tip: "abc", found: true}, nil)
	fix := "item 7 of project p has no worktree, so merge cannot run git for it. Make the worktree of the item first"

	_, err := host.Git(t.Context(), "p", "7")
	require.EqualError(t, err, fix)

	_, err = host.Checkout(t.Context(), "p", "7", "abc")
	require.EqualError(t, err, fix)

	_, err = host.Tip(t.Context(), "p", "7", "main")
	require.EqualError(t, err, fix)
}

func TestBatchHost_IntentReadsAnOpenPullRequestOnce(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var reads int

	f := hostForge{state: "OPEN", viewHead: gated, listed: []model.PullRequest{{Number: 9}}, pullReads: &reads}

	intent, err := hostOver("", f, nil).Intent(t.Context(), "p", "item", "o/r", "main", "tip-tree", now)

	require.NoError(t, err)
	assert.Equal(t, 1, reads, "the intent reads the pull request once")
	assert.Equal(t, 9, intent.PR)
	assert.Equal(t, gated, intent.Head)
	assert.Equal(t, "main", intent.Base)
	assert.Equal(t, "tip-tree", intent.BaseTipTree)
	assert.Equal(t, "OPEN", intent.State)
	assert.Equal(t, now, intent.Time)
}

func TestBatchHost_IntentIsNoRefusalOfAListOrALinkThatTheMergeDoesNotRead(t *testing.T) {
	t.Parallel()

	const lists = `"isDraft":false,"labels":[],"assignees":[]`

	for name, raw := range map[string]string{
		"no assignee list": `{"state":"OPEN","headRefOid":"` + gated + `","baseRefName":"main","isDraft":false,"labels":[]}`,
		"no label list":    `{"state":"OPEN","headRefOid":"` + gated + `","baseRefName":"main","isDraft":false,"assignees":[]}`,
		"no draft flag":    `{"state":"OPEN","headRefOid":"` + gated + `","baseRefName":"main","labels":[],"assignees":[]}`,
		"a bad body link":  `{"state":"OPEN","headRefOid":"` + gated + `","baseRefName":"main",` + lists + `,"body":"refs #99999999999999999999"}`,
	} {
		f := hostForge{state: "OPEN", viewHead: gated, listed: []model.PullRequest{{Number: 9}}, rawRead: raw}

		intent, err := hostOver("", f, nil).Intent(t.Context(), "p", "item", "o/r", "main", "tip-tree", time.Time{})

		require.NoError(t, err, name)
		assert.Equal(t, gated, intent.Head, name)
	}

	f := hostForge{state: "OPEN", viewHead: gated, listed: []model.PullRequest{{Number: 9}}, rawRead: `{"state":"OPEN","baseRefName":"main","isDraft":false,"labels":[],"assignees":[]}`}

	_, err := hostOver("", f, nil).Intent(t.Context(), "p", "item", "o/r", "main", "tip-tree", time.Time{})

	require.ErrorContains(t, err, "the read has no head", "a read with no head stays a refusal")
}

func TestBatchHost_IntentRefusesAPullRequestThatIsNotOpen(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"MERGED", "CLOSED"} {
		f := hostForge{state: state, viewHead: gated, listed: []model.PullRequest{{Number: 9}}}

		_, err := hostOver("", f, nil).Intent(t.Context(), "p", "item", "o/r", "main", "tip-tree", time.Time{})

		require.ErrorContains(t, err, "the pull request of item item is "+state+", not OPEN", state)
		require.ErrorIs(t, err, flow.ErrMemberNotOpen, state)
	}
}

func TestBatchHost_IntentFailsForAReadThatFailsAndForAPullRequestThatIsNotFound(t *testing.T) {
	t.Parallel()

	listed := []model.PullRequest{{Number: 9}}

	_, err := hostOver("", hostForge{viewErr: errFake, listed: listed}, nil).Intent(t.Context(), "p", "item", "o/r", "main", "t", time.Time{})
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the pull request of item item")

	_, err = hostOver("", hostForge{}, nil).Intent(t.Context(), "p", "item", "o/r", "main", "t", time.Time{})
	require.ErrorIs(t, err, forge.ErrNoPull)

	_, err = hostOver("", hostForge{state: "OPEN", listed: listed}, nil).Intent(t.Context(), "p", "item", "other/repo", "main", "t", time.Time{})
	require.ErrorContains(t, err, "other/repo")
}

func TestBatchHost_IntentRefusesAReadWithNoStateHeadOrBase(t *testing.T) {
	t.Parallel()

	listed := []model.PullRequest{{Number: 9}}

	for name, f := range map[string]hostForge{
		"state": {viewHead: gated, listed: listed},
		"head":  {state: "OPEN", listed: listed},
	} {
		_, err := hostOver("", f, nil).Intent(t.Context(), "p", "item", "o/r", "main", "tip-tree", time.Time{})

		require.ErrorContains(t, err, "the read has no "+name, name)
	}
}

func TestBatchHost_IntentRefusesAReadThatAnswersForAnotherPullRequest(t *testing.T) {
	t.Parallel()

	f := hostForge{state: "OPEN", viewHead: gated, listed: []model.PullRequest{{Number: 9}}, readNumber: 4}

	_, err := hostOver("", f, nil).Intent(t.Context(), "p", "item", "o/r", "main", "tip-tree", time.Time{})

	require.ErrorContains(t, err, "answered for pull request 4, not 9")
}

func TestBatchHost_PullReadsTheDraftFlagAndTheHeadOfTheOpenPullRequestThroughTheStandingReview(t *testing.T) {
	t.Parallel()

	draft := true
	f := hostForge{state: " open ", draft: &draft, viewHead: gated, listed: []model.PullRequest{{Number: 9}}}

	got, err := hostOver("", f, nil).Pull(t.Context(), "p", "item", "o/r", "")

	require.NoError(t, err)
	assert.Equal(t, 9, got.Number)
	assert.True(t, got.Draft)
	assert.Equal(t, gated, got.Head)
	assert.False(t, got.Merged || got.Closed)
	assert.Empty(t, got.Unread)
}

func TestBatchHost_PullShowsAClosedPullRequestAsClosedAndRefusesAnItemWithNoPullRequest(t *testing.T) {
	t.Parallel()

	got, err := hostOver("", hostForge{closed: []model.PullRequest{{Number: 4}}}, nil).Pull(t.Context(), "p", "item", "o/r", "")

	require.NoError(t, err)
	assert.Equal(t, review.Standing{Number: 4, Closed: true}, got)

	_, err = hostOver("", hostForge{}, nil).Pull(t.Context(), "p", "item", "o/r", "")
	require.ErrorIs(t, err, forge.ErrNoPull, "no open, merged or closed pull request is an error, not a closed one")

	_, err = hostOver("", hostForge{listErr: errFake}, nil).Pull(t.Context(), "p", "item", "o/r", "")
	require.ErrorIs(t, err, errFake)
}

func TestBatchHost_PullFailsForAProjectWithNoForgeAndPutsAViewThatFailsIntoUnread(t *testing.T) {
	t.Parallel()

	listed := []model.PullRequest{{Number: 9}}

	got, err := hostOver("", hostForge{viewErr: errFake, listed: listed}, nil).Pull(t.Context(), "p", "item", "o/r", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"pull request"}, got.Unread, "a read that fails is a reason in the standing, never an empty fact")

	_, err = hostOver("", hostForge{}, nil).Pull(t.Context(), "p", "item", "other/repo", "")
	require.ErrorContains(t, err, "other/repo")
}

func TestBatchHost_PullOfAProjectThatTheConfigLacksFails(t *testing.T) {
	t.Parallel()

	_, err := hostOver("", hostForge{}, nil).Pull(t.Context(), "unknown", "item", "o/r", "")

	require.Error(t, err)
}

func TestBatchHost_SetDraftMarksThePullRequestOfTheItem(t *testing.T) {
	t.Parallel()

	calls := []string{}
	f := hostForge{listed: []model.PullRequest{{Number: 9}}, drafted: &calls}

	require.NoError(t, hostOver("", f, nil).SetDraft(t.Context(), "p", "item", "o/r", true))
	require.NoError(t, hostOver("", f, nil).SetDraft(t.Context(), "p", "item", "o/r", false))
	assert.Equal(t, []string{"9:true", "9:false"}, calls)

	f.setDraftErr = errFake
	require.ErrorIs(t, hostOver("", f, nil).SetDraft(t.Context(), "p", "item", "o/r", true), errFake)

	require.ErrorIs(t, hostOver("", hostForge{}, nil).SetDraft(t.Context(), "p", "item", "o/r", true), forge.ErrNoPull)
}

// OpenChangeRequests gives f.changes change requests, or the error of the read.
func (f hostForge) OpenChangeRequests(context.Context, int) ([]model.ChangeRequest, error) {
	return make([]model.ChangeRequest, f.changes), f.changesErr
}

// UnresolvedThreads gives f.threads threads, or the error of the read.
func (f hostForge) UnresolvedThreads(context.Context, int) ([]model.ReviewThread, error) {
	return make([]model.ReviewThread, f.threads), f.threadsErr
}

// CommitTree gives the tree of a commit from f.trees, or the error of the read.
func (f hostForge) CommitTree(_ context.Context, commit string) (string, error) {
	return f.trees[commit], f.treeErr
}

func TestBatchHost_PullReadsTheConflictTheChangeRequestsAndTheThreadsOfAnOpenPullRequest(t *testing.T) {
	t.Parallel()

	draft := false
	f := hostForge{state: "OPEN", draft: &draft, viewHead: gated, mergeable: "CONFLICTING", changes: 2, threads: 3, listed: []model.PullRequest{{Number: 9}}}

	got, err := hostOver("", f, nil).Pull(t.Context(), "p", "item", "o/r", "")

	require.NoError(t, err)
	assert.True(t, got.Conflicting)
	assert.Len(t, got.ChangeRequests, 2)
	assert.Equal(t, 3, got.UnresolvedThreads)
}

func TestBatchHost_PullShowsAMergedPullRequestAsMerged(t *testing.T) {
	t.Parallel()

	draft := false
	f := hostForge{state: "MERGED", draft: &draft, viewHead: gated, listed: []model.PullRequest{{Number: 9}}}

	got, err := hostOver("", f, nil).Pull(t.Context(), "p", "item", "o/r", "")

	require.NoError(t, err)
	assert.True(t, got.Merged)
	assert.Equal(t, gated, got.Head)
}

func TestBatchHost_PullPutsTheChangeRequestsAndTheThreadsThatCannotBeReadIntoUnread(t *testing.T) {
	t.Parallel()

	draft := false
	listed := []model.PullRequest{{Number: 9}}

	got, err := hostOver("", hostForge{state: "OPEN", draft: &draft, changesErr: errFake, listed: listed}, nil).Pull(t.Context(), "p", "item", "o/r", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"change requests"}, got.Unread)

	got, err = hostOver("", hostForge{state: "OPEN", draft: &draft, threadsErr: errFake, listed: listed}, nil).Pull(t.Context(), "p", "item", "o/r", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"review threads"}, got.Unread)
}

func TestBatchHost_PullComparesTheTreeOfAHeadThatIsNotTheReviewedCommit(t *testing.T) {
	t.Parallel()

	draft := false
	listed := []model.PullRequest{{Number: 9}}
	trees := map[string]string{"a": "t1", gated: "t1", "c": "t2"}

	same, err := hostOver("", hostForge{state: "OPEN", draft: &draft, viewHead: gated, listed: listed, trees: trees}, nil).Pull(t.Context(), "p", "item", "o/r", "a")
	require.NoError(t, err)
	assert.Equal(t, "a", same.Rewritten, "the head holds the tree of the reviewed commit")

	other, err := hostOver("", hostForge{state: "OPEN", draft: &draft, viewHead: gated, listed: listed, trees: trees}, nil).Pull(t.Context(), "p", "item", "o/r", "c")
	require.NoError(t, err)
	assert.Empty(t, other.Rewritten)

	itself, err := hostOver("", hostForge{state: "OPEN", draft: &draft, viewHead: gated, listed: listed, treeErr: errFake}, nil).Pull(t.Context(), "p", "item", "o/r", gated)
	require.NoError(t, err)
	assert.Empty(t, itself.Unread, "a head that is the reviewed commit needs no tree read")

	failed, err := hostOver("", hostForge{state: "OPEN", draft: &draft, viewHead: gated, listed: listed, treeErr: errFake}, nil).Pull(t.Context(), "p", "item", "o/r", "a")
	require.NoError(t, err)
	assert.Equal(t, []string{"tree of the reviewed commit", "tree of the head"}, failed.Unread)
}

// TreeBlobs is the other half of the tree reads of a forge. A tree comparison reads no files, so it gives none.
func (f hostForge) TreeBlobs(context.Context, string) (map[string]string, error) { return nil, nil } //nolint:nilnil // a fake that serves no file
