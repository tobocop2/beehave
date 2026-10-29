package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

// hostTrees gives the one folder that the host tests use, or an error.
type hostTrees struct {
	dir string
	err error
}

func (t hostTrees) Path(context.Context, string, string) (string, error) { return t.dir, t.err }

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
}

func (f hostForge) BranchRef(context.Context, string) (string, bool, error) {
	return f.tip, f.found, f.tipErr
}

func (f hostForge) PRList(context.Context, int, ...string) ([]model.PullRequest, error) {
	return f.listed, f.listErr
}

func (f hostForge) PRView(_ context.Context, number int, _ ...string) (model.PullRequest, error) {
	if f.viewErr != nil {
		return model.PullRequest{}, f.viewErr
	}

	head, base := f.viewHead, "main"
	view := model.PullRequest{Number: number, HeadRefOid: &head, BaseRefName: &base}

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
			local: func(binary, folder string, timeout time.Duration) proc.Runner {
				return proc.ExecRunner{Binary: binary, Dir: folder, Timeout: timeout}
			},
		},
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
	hostGit(t, dir, "remote", "remove", "origin")

	_, err := hostOver(dir, hostForge{tip: "abc", found: true}, nil).Tip(t.Context(), "p", "7", "main")
	require.ErrorContains(t, err, "git fetch in "+dir)

	hostGit(t, dir, "remote", "add", "origin", ".")

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
	assert.Equal(t, "obee obee@localhost obee obee@localhost", hostGit(t, dir, "log", "-1", "--format=%an %ae %cn %ce", made))
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
	fix := "item 7 of project p has no worktree, so batch_merge cannot run git for it. Make the worktree of the item first"

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
	f := hostForge{state: "OPEN", viewHead: gated, listed: []model.PullRequest{{Number: 9}}}

	intent, err := hostOver("", f, nil).Intent(t.Context(), "p", "item", "o/r", "main", "tip-tree", now)

	require.NoError(t, err)
	assert.Equal(t, 9, intent.PR)
	assert.Equal(t, gated, intent.Head)
	assert.Equal(t, "main", intent.Base)
	assert.Equal(t, "tip-tree", intent.BaseTipTree)
	assert.Equal(t, "OPEN", intent.State)
	assert.Equal(t, now, intent.Time)
}

func TestBatchHost_IntentRefusesAPullRequestThatIsNotOpen(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"MERGED", "CLOSED", ""} {
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
	require.ErrorIs(t, err, errNoPull)

	_, err = hostOver("", hostForge{state: "OPEN", listed: listed}, nil).Intent(t.Context(), "p", "item", "other/repo", "main", "t", time.Time{})
	require.ErrorContains(t, err, "other/repo")

	_, err = hostOver("", hostForge{state: "OPEN", listed: listed}, nil).Intent(t.Context(), "p", "item", "o/r", "main", "t", time.Time{})
	require.ErrorContains(t, err, "cannot make an intent", "a read with no head proves nothing")
}
