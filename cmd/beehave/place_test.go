package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

const trackerItem = "12"

// workIn makes a commit in the worktree dir and returns its id.
func workIn(t *testing.T, dir, name string) string {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600))
	runGit(t, dir, "add", name)
	runGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", name)

	return gitOut(t, dir, "rev-parse", "HEAD")
}

// removeTree takes the worktree dir away and leaves its commits in the clone, as a release does.
func (r roundRig) removeTree(t *testing.T, dir string) {
	t.Helper()

	runGit(t, r.clone(), "worktree", "remove", "--force", dir)
}

// chmodTest sets the mode of a folder that the test made.
func chmodTest(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()

	require.NoError(t, exectest.ChmodFolder(dir, mode))
}

func TestWorktrees_PlaceCutsAMissingRoundWorktreeDetachedAtTheHeadAndNeverAtTheTipOfTheBase(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	head := workIn(t, dir, "one")
	workIn(t, dir, "two")
	rig.removeTree(t, dir)
	commitOn(t, rig.origin, "integration/r1")

	require.NoError(t, rig.trees.Place(t.Context(), "p", roundItem, head, ""))

	assert.Equal(t, head, gitOut(t, dir, "rev-parse", "HEAD"))
	assert.Empty(t, gitOut(t, dir, "branch", "--show-current"), "the round worktree is detached")
}

func TestWorktrees_PlaceMovesAKeptRoundWorktreeBackToTheHeadWhenARemoteBranchHoldsTheWorkItLeaves(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	head := workIn(t, dir, "one")
	workIn(t, dir, "two")
	runGit(t, dir, "push", "-q", "origin", "HEAD:refs/heads/saved")

	require.NoError(t, rig.trees.Place(t.Context(), "p", roundItem, head, ""))

	assert.Equal(t, head, gitOut(t, dir, "rev-parse", "HEAD"))
}

func TestWorktrees_PlaceKeepsAKeptRoundWorktreeThatIsAlreadyAtTheHeadEvenWhenNoRemoteBranchHoldsIt(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	head := workIn(t, dir, "one")

	require.NoError(t, rig.trees.Place(t.Context(), "p", roundItem, head, ""), "the work of the steps before the fork is what the fork starts from")
	assert.Equal(t, head, gitOut(t, dir, "rev-parse", "HEAD"))
}

func TestWorktrees_PlaceRefusesAKeptRoundWorktreeWhoseLaterCommitsNoRemoteBranchHoldsAndLeavesItWhereItIs(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	head := workIn(t, dir, "one")
	later := workIn(t, dir, "two")

	err = rig.trees.Place(t.Context(), "p", roundItem, head, "")

	require.ErrorIs(t, err, errKeptChanges)
	require.ErrorContains(t, err, "two", "the error names the commit that the move would lose")
	assert.Equal(t, later, gitOut(t, dir, "rev-parse", "HEAD"))
}

func TestWorktrees_PlaceRefusesAWorktreeWithChangesAndLeavesItWhereItIs(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	head := workIn(t, dir, "one")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o600))

	err = rig.trees.Place(t.Context(), "p", roundItem, head, "")

	require.ErrorIs(t, err, errKeptChanges)
	assert.FileExists(t, filepath.Join(dir, "stray.txt"))
}

func TestWorktrees_PlaceRefusesACommitThatTheCloneDoesNotHoldAndNamesIt(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	_, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	const missing = "0123456789012345678901234567890123456789"

	err = rig.trees.Place(t.Context(), "p", roundItem, missing, "")

	require.ErrorContains(t, err, missing)
	require.ErrorContains(t, err, "beehave run "+roundItem, "the error says how to start the item again")
}

func TestWorktrees_PlaceGivesATrackerItemABranchOfItsOwnAtTheHeadAndLeavesTheBranchOfTheItem(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", trackerItem, "", "")
	require.NoError(t, err)

	head := workIn(t, dir, "one")
	later := workIn(t, dir, "two")

	require.NoError(t, rig.trees.Place(t.Context(), "p", trackerItem, head, "12-r2"))

	assert.Equal(t, "12-r2", gitOut(t, dir, "branch", "--show-current"))
	assert.Equal(t, head, gitOut(t, dir, "rev-parse", "HEAD"))
	assert.Equal(t, later, gitOut(t, dir, "rev-parse", "refs/heads/"+model.ItemBranch(trackerItem)), "the old branch keeps its commits")

	workIn(t, dir, "three")

	require.NoError(t, rig.trees.Place(t.Context(), "p", trackerItem, head, "12-r2"), "a second try of the same attempt starts again at the head")
	assert.Equal(t, head, gitOut(t, dir, "rev-parse", "HEAD"))
}

func TestWorktrees_PlaceCutsAMissingTrackerWorktreeOnTheBranchOfTheAttempt(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", trackerItem, "", "")
	require.NoError(t, err)

	head := workIn(t, dir, "one")
	rig.removeTree(t, dir)

	require.NoError(t, rig.trees.Place(t.Context(), "p", trackerItem, head, "12-r2"))

	assert.Equal(t, "12-r2", gitOut(t, dir, "branch", "--show-current"))
	assert.Equal(t, head, gitOut(t, dir, "rev-parse", "HEAD"))

	rig.removeTree(t, dir)

	require.NoError(t, rig.trees.Place(t.Context(), "p", trackerItem, head, "12-r2"), "the branch of a try that failed is cut again")
	assert.Equal(t, head, gitOut(t, dir, "rev-parse", "HEAD"))
}

func TestWorktrees_PlaceCutsATrackerWorktreeAgainThatSomeoneRemovedWithItsRecordKept(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", trackerItem, "", "")
	require.NoError(t, err)

	head := workIn(t, dir, "one")
	require.NoError(t, rig.trees.Place(t.Context(), "p", trackerItem, head, "12-r2"))
	require.NoError(t, os.RemoveAll(dir))

	require.NoError(t, rig.trees.Place(t.Context(), "p", trackerItem, head, "12-r2"))

	assert.Equal(t, "12-r2", gitOut(t, dir, "branch", "--show-current"))
	assert.Equal(t, head, gitOut(t, dir, "rev-parse", "HEAD"))
}

func TestWorktrees_PlaceFindsNothingToPlaceInAProjectWithNoRepository(t *testing.T) {
	t.Parallel()

	projects := map[string]config.Project{"plain": {Project: model.Project{Name: "plain"}}}
	trees := newWorktrees(projects, flow.Options{StateDir: evaluated(t, t.TempDir())}, nil, nil)

	require.NoError(t, trees.Place(t.Context(), "plain", trackerItem, "abc", "12-r2"))
	assert.NoDirExists(t, filepath.Join(trees.root, "plain"), "no folder was made")
}

func TestWorktrees_PlaceRefusesAFolderThatCannotBeRead(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	head := workIn(t, dir, "one")
	loop := filepath.Join(rig.trees.root, "p", "loop")
	require.NoError(t, os.Symlink(loop, loop))

	require.ErrorContains(t, rig.trees.Place(t.Context(), "p", "loop", head, "loop-r2"), "read the folder")
}

func TestWorktrees_AGitThatCannotReadTheStatusOfAWorktreeStopsTheMoveAndAWorktreeWithChangesIsNamed(t *testing.T) {
	t.Parallel()

	broken := worktrees{git: func(string) proc.Runner { return scripted{result: model.CommandResult{ExitCode: 128, Stderr: "boom"}} }}

	require.ErrorContains(t, broken.refuseChanges(t.Context(), "clone", "dir", "12", "a run left"), "boom")
}

func TestWorktrees_PlaceRefusesWhenTheFolderOfTheWorktreesCannotBeWritten(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	head := workIn(t, dir, "one")
	rig.removeTree(t, dir)
	require.NoError(t, os.RemoveAll(filepath.Join(rig.trees.root, "p")))
	chmodTest(t, rig.trees.root, 0o500)
	t.Cleanup(func() { chmodTest(t, rig.trees.root, 0o700) })

	require.ErrorContains(t, rig.trees.Place(t.Context(), "p", trackerItem, head, "12-r2"), "make the folder for the worktree of item 12")
}

func TestWorktrees_PlaceRefusesWhenGitCannotDropTheRecordOfARemovedTrackerWorktree(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", trackerItem, "", "")
	require.NoError(t, err)

	head := workIn(t, dir, "one")
	require.NoError(t, os.RemoveAll(dir))

	real := rig.trees.git
	rig.trees.git = func(folder string) proc.Runner { return pruneRefused{Runner: real(folder)} }

	require.ErrorContains(t, rig.trees.Place(t.Context(), "p", trackerItem, head, "12-r2"), "fatal: refused")
}

// pruneRefused is a git runner whose worktree prune exits with code 128.
type pruneRefused struct{ proc.Runner }

func (r pruneRefused) Run(ctx context.Context, args []string) model.CommandResult {
	if slices.Equal(args, []string{"worktree", "prune"}) {
		return model.CommandResult{ExitCode: 128, Stderr: "fatal: refused"}
	}

	return r.Runner.Run(ctx, args)
}

func TestWorktrees_PlaceRefusesAnUnknownProject(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	require.ErrorIs(t, rig.trees.Place(t.Context(), "nope", trackerItem, "abc", "12-r2"), errUnknownProject)
}
