package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

func TestWorktrees_BranchMoveInOneLaneLeavesOtherLanes(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	first, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "")
	require.NoError(t, err)

	second, err := rig.trees.Ensure(t.Context(), "p", "bb-2", "", "")
	require.NoError(t, err)

	branch := model.ItemBranch("bb-2")
	before := gitOut(t, second, "rev-parse", "refs/heads/"+branch)
	moved := workIn(t, first, "one")

	runGit(t, first, "update-ref", "refs/heads/"+branch, moved)
	runGit(t, first, "branch", "-f", "ctl-"+branch, moved)

	assert.Equal(t, moved, gitOut(t, first, "rev-parse", "refs/heads/"+branch), "the move took effect in the lane that ran it")
	assert.Equal(t, before, gitOut(t, second, "rev-parse", "refs/heads/"+branch), "the branch of the other lane did not move")
	assert.Empty(t, gitOut(t, second, "branch", "--list", "ctl-"+branch), "the branch that the first lane made is not in the other lane")
	assert.Empty(t, gitOut(t, rig.clone(), "branch", "--list", branch), "the clone of the project holds no branch of an item")
}

func TestWorktrees_EnsureCutsTheLaneWithItsOwnRefsAndTheAddressOfTheRemote(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "")
	require.NoError(t, err)

	lane := laneOf(rig.clone(), "bb-1")

	assert.Equal(t, rig.origin, gitOut(t, lane, "config", "remote.origin.url"), "a fetch in a step reaches the remote, not the clone of the project")
	assert.Equal(t, gitOut(t, rig.origin, "rev-parse", "main"), gitOut(t, lane, "rev-parse", "refs/remotes/origin/main"))
	assert.Equal(t, filepath.Join(lane, ".git"), gitOut(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	assert.Equal(t, []string{filepath.Join(lane, ".git")}, sharedGitDirs(gitexec.NewRepositories(rig.trees.repos), dir), "the sandbox opens the git folder of the lane, not the one of the project")
}

func TestWorktrees_EnsureRefusesAWorktreeOfTheSharedLayoutAndNamesTheCommandThatRemovesIt(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	_, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "")
	require.NoError(t, err)

	old := filepath.Join(rig.trees.root, "p", "bb-2")
	runGit(t, rig.clone(), "worktree", "add", "-b", "old-layout", old, "origin/main")

	got, err := rig.trees.Ensure(t.Context(), "p", "bb-2", "", "")

	require.ErrorIs(t, err, errSharedLayout)
	require.ErrorContains(t, err, "git -C "+rig.clone()+" worktree remove --force "+old)
	assert.Empty(t, got)
	assert.DirExists(t, old, "the refusal deletes nothing")
}

func TestWorktrees_AFolderThatCannotBeReadIsNoLane(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	clone := rig.clone()
	long := strings.Repeat("a", 300)

	require.NoError(t, os.MkdirAll(rig.trees.repos, 0o700))

	t.Run("the lane of a worktree", func(t *testing.T) {
		t.Parallel()

		err := refuseSharedLayout(clone, filepath.Join(rig.trees.root, "p", long), long)

		require.ErrorContains(t, err, "read the clone of item "+long)
		assert.NotErrorIs(t, err, errSharedLayout)
	})

	t.Run("the lane to cut", func(t *testing.T) {
		t.Parallel()

		err := rig.trees.cut(t.Context(), "p", clone, laneOf(clone, long))

		require.ErrorContains(t, err, "read the clone ")
	})
}

func TestWorktrees_FetchCopiesTheNewBranchesIntoEachLaneOfTheProject(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	_, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "")
	require.NoError(t, err)

	_, err = rig.trees.Ensure(t.Context(), "p", "bb-2", "", "")
	require.NoError(t, err)

	tip := commitOn(t, rig.origin, "late-branch")

	require.NoError(t, rig.trees.Fetch(t.Context(), "p"))

	for _, item := range []string{"bb-1", "bb-2"} {
		assert.Equal(t, tip, gitOut(t, laneOf(rig.clone(), item), "rev-parse", "refs/remotes/origin/late-branch"), "the lane of "+item+" sees the branch that the fetch brought")
	}
}

func TestWorktrees_SyncLanesNamesTheFolderThatFails(t *testing.T) {
	t.Parallel()

	t.Run("the folder of the clones is gone", func(t *testing.T) {
		t.Parallel()

		rig := newRoundRig(t)

		err := rig.trees.syncLanes(t.Context(), filepath.Join(t.TempDir(), "gone", "p"))

		require.ErrorContains(t, err, "read the folder of the clones")
	})

	t.Run("a lane is no repository", func(t *testing.T) {
		t.Parallel()

		rig := newRoundRig(t)
		require.NoError(t, os.MkdirAll(laneOf(rig.clone(), "broken"), 0o700))

		err := rig.trees.syncLanes(t.Context(), rig.clone())

		require.ErrorContains(t, err, "git fetch in "+laneOf(rig.clone(), "broken"))
	})

	t.Run("a file and a folder of another project are no lane", func(t *testing.T) {
		t.Parallel()

		rig := newRoundRig(t)
		require.NoError(t, os.MkdirAll(filepath.Join(rig.trees.repos, "other.lane.x"), 0o700))
		require.NoError(t, os.WriteFile(laneOf(rig.clone(), "file"), nil, 0o600))

		require.NoError(t, rig.trees.syncLanes(t.Context(), rig.clone()))
	})
}
