package flow

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/proc"
)

// remoteSleepSeconds is how long the fake remote of the timeout test stays silent. A call that waits for the remote ends no sooner than this.
const remoteSleepSeconds = 60

// releaseSlack is the time that a loaded machine may add to a call that has a short limit. It stays below releaseTimeout.
const releaseSlack = 20 * time.Second

// gitIn runs git in dir with a fixed identity and no user config, and returns the trimmed output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := proc.GroupCommand(t.Context(), "git", args, dir)
	cmd.Env = append(cmd.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

// commitIn writes a file and commits it.
func commitIn(t *testing.T, dir, name string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600))
	gitIn(t, dir, "add", name)
	gitIn(t, dir, "commit", "-m", name)
}

// repos is a repository with a bare remote and the worktree of one item, pushed.
type repos struct {
	remote string
	main   string
	tree   string
}

func newRepos(t *testing.T) repos {
	t.Helper()

	root := t.TempDir()
	r := repos{remote: filepath.Join(root, "remote.git"), main: filepath.Join(root, "main"), tree: filepath.Join(root, "item-12")}

	require.NoError(t, os.Mkdir(r.remote, 0o700))
	require.NoError(t, os.Mkdir(r.main, 0o700))
	gitIn(t, r.remote, "init", "--bare", "-b", "main")
	gitIn(t, r.main, "init", "-b", "main")
	commitIn(t, r.main, "first")
	gitIn(t, r.main, "remote", "add", "origin", r.remote)
	gitIn(t, r.main, "push", "origin", "main")
	gitIn(t, r.main, "worktree", "add", "-b", "item-12", r.tree)
	commitIn(t, r.tree, "work")
	gitIn(t, r.tree, "push", "origin", "item-12")

	return r
}

func removeTreeOf(t *testing.T, dir string, limit time.Duration) (string, error) {
	t.Helper()

	engine := &Engine{deps: Deps{Trees: fakeTrees{dir: dir}, Clock: clock{Release: limit}}}

	return engine.removeTree(t.Context(), testProject, testItem)
}

func TestRemoveTree_RemovesACleanTreeWhoseHeadIsOnTheRemote(t *testing.T) {
	t.Parallel()

	r := newRepos(t)

	kept, err := removeTreeOf(t, r.tree, 0)

	require.NoError(t, err)
	assert.Empty(t, kept)
	assert.NoDirExists(t, r.tree)
	assert.DirExists(t, r.main)
}

func TestRemoveTree_KeepsATreeWithChangesThatAreNotCommitted(t *testing.T) {
	t.Parallel()

	tests := map[string]string{"an_untracked_file": "new", "an_edited_file": "work"}

	for name, file := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := newRepos(t)
			require.NoError(t, os.WriteFile(filepath.Join(r.tree, file), []byte("changed"), 0o600))

			kept, err := removeTreeOf(t, r.tree, 0)

			require.NoError(t, err)
			assert.Contains(t, kept, "not committed")
			assert.DirExists(t, r.tree)
		})
	}
}

func TestRemoveTree_KeepsATreeWhoseHeadIsNotOnTheRemote(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	commitIn(t, r.tree, "unpushed")

	kept, err := removeTreeOf(t, r.tree, 0)

	require.NoError(t, err)
	assert.Contains(t, kept, "not the tip of a branch on the remote")
	assert.DirExists(t, r.tree)
}

func TestRemoveTree_ReadsTheRemoteAndNotTheLocalTrackingRefs(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	// The push left refs/remotes/origin/item-12 at HEAD. The remote then loses the commit.
	gitIn(t, r.remote, "update-ref", "refs/heads/item-12", gitIn(t, r.remote, "rev-parse", "main"))
	require.Equal(t, gitIn(t, r.tree, "rev-parse", "HEAD"), gitIn(t, r.tree, "rev-parse", "refs/remotes/origin/item-12"), "the local ref still shows the push")

	kept, err := removeTreeOf(t, r.tree, 0)

	require.NoError(t, err)
	assert.Contains(t, kept, "not the tip of a branch on the remote")
	assert.DirExists(t, r.tree)
}

func TestRemoveTree_KeepsTheTreeWhenTheRemoteDoesNotAnswerInTime(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	// The fake trees allow the ext transport for the read of the branches; a protocol setting in the clone config is one the engine refuses.
	gitIn(t, r.tree, "remote", "set-url", "origin", "ext::sleep "+strconv.Itoa(remoteSleepSeconds))

	const limit = 300 * time.Millisecond

	started := time.Now()
	kept, err := removeTreeOf(t, r.tree, limit)

	require.NoError(t, err)
	assert.Contains(t, kept, "in time")
	// A call that ignores the clock limit ends at releaseTimeout, so the slack must stay below it.
	assert.Less(t, time.Since(started), limit+releaseSlack, "the call ends at the clock limit, not at the default timeout or when the remote answers")
	assert.DirExists(t, r.tree)
}

func TestRemoveTree_KeepsTheTreeWhenTheBranchesOfTheRemoteCannotBeRead(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	engine := &Engine{deps: Deps{Trees: fakeTrees{dir: r.tree, headsErr: errFake}}}

	kept, err := engine.removeTree(t.Context(), testProject, testItem)

	require.NoError(t, err)
	assert.Equal(t, "git could not read the branches of the remote: "+errFake.Error(), kept, "a tip that the remote read did not confirm is unknown")
	assert.DirExists(t, r.tree, "the release keeps the tree, although the branches it got show its head")
}

func TestRemoveTree_KeepsTheTreeOnAGitError(t *testing.T) {
	t.Parallel()

	kept, err := removeTreeOf(t, t.TempDir(), 0)

	require.NoError(t, err)
	assert.Contains(t, kept, "git could not read the state of the worktree")
}

func TestRemoveTree_KeepsTheTreeWhenGitRefusesToRemoveIt(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	gitIn(t, r.main, "worktree", "lock", r.tree)

	kept, err := removeTreeOf(t, r.tree, 0)

	require.NoError(t, err)
	assert.Contains(t, kept, "git could not remove the worktree")
	assert.DirExists(t, r.tree)
}

func TestRemoveTree_NamesAnItemWithoutAWorktreeAndFailsWhenTheLookupFails(t *testing.T) {
	t.Parallel()

	kept, err := removeTreeOf(t, "", 0)
	require.NoError(t, err)
	assert.Equal(t, "item has no worktree", kept)

	engine := &Engine{deps: Deps{Trees: fakeTrees{err: errFake}}}
	_, err = engine.removeTree(t.Context(), testProject, testItem)
	require.ErrorIs(t, err, errFake)
}

func TestRun_AKeptWorktreeDoesNotFailTheRunAndAFailedLookupKeepsTheClaimRelease(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	commitIn(t, r.tree, "unpushed")

	harness := newHarness(t)
	harness.trees.dir = r.tree
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)
	assert.DirExists(t, r.tree)
	assert.Equal(t, []string{"claim:12", "release:12"}, harness.life.seen())

	failing := newHarness(t)
	failing.trees.err = errFake
	_, err = failing.engine(t).Run(testItem, testProject)
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "worktree of item 12")
	assert.Equal(t, []string{"claim:12", "release:12"}, failing.life.seen())
}

// lockedTrees gives one worktree, as the worktrees of the engine do under the lock of the clone. It records whether the folder of the worktree was still
// there when WithTree gave the lock back.
type lockedTrees struct {
	dir     string
	stayed  *bool
	unlocks *int
}

func (l lockedTrees) WithTree(_ context.Context, _, _ string, fn func(dir string)) error {
	fn(l.dir)

	_, err := os.Stat(l.dir)
	*l.stayed = err == nil
	*l.unlocks++

	return nil
}

func (lockedTrees) Git(dir string) proc.Runner { return engineGit(dir) }

func (addressTrees) Git(dir string) proc.Runner { return engineGit(dir) }

func (l lockedTrees) Heads(ctx context.Context, _ string) (string, error) {
	return git(ctx, engineGit(l.dir), "ls-remote", "--heads", remoteName)
}

// addressTrees gives one worktree and reads the branches of the remote from the address that the engine holds, as the worktrees of the engine do.
type addressTrees struct {
	dir     string
	address string
}

func (a addressTrees) WithTree(_ context.Context, _, _ string, fn func(dir string)) error {
	fn(a.dir)

	return nil
}

func (a addressTrees) Heads(ctx context.Context, _ string) (string, error) {
	return git(ctx, engineGit(a.dir), "ls-remote", "--heads", a.address)
}

func TestRemoveTree_ReadsTheBranchesOfTheRemoteFromTheTreesAndNotFromTheRemoteThatTheCloneNames(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	decoy := filepath.Join(t.TempDir(), "decoy.git")
	require.NoError(t, os.Mkdir(decoy, 0o700))
	gitIn(t, decoy, "init", "--bare", "-b", "main")
	gitIn(t, r.main, "remote", "set-url", "origin", decoy)

	kept, err := ReleaseDeps{Trees: addressTrees{dir: r.tree, address: r.remote}}.removeTree(t.Context(), testProject, testItem)

	require.NoError(t, err)
	assert.Empty(t, kept, "the release reads the remote at the address of the trees, which holds the head of the item")
	assert.NoDirExists(t, r.tree)
}

func TestRemoveTree_RemovesTheWorktreeBeforeItGivesTheLockOfTheCloneBack(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	stayed, unlocks := true, 0

	kept, err := ReleaseDeps{Trees: lockedTrees{dir: r.tree, stayed: &stayed, unlocks: &unlocks}}.removeTree(t.Context(), testProject, testItem)

	require.NoError(t, err)
	assert.Empty(t, kept)
	assert.Equal(t, 1, unlocks)
	assert.False(t, stayed, "the git worktree remove ran after the release gave the lock of the clone back, so a sibling add could race it")
	assert.NoDirExists(t, r.tree)
}

func TestRun_AnApprovedItemLosesItsPushedWorktree(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	harness := newHarness(t)
	harness.trees.dir = r.tree

	_, err := harness.engine(t).Run(testItem, testProject)

	require.NoError(t, err)
	assert.NoDirExists(t, r.tree)
}
