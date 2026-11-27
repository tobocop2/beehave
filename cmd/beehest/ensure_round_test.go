package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc"
	"github.com/tobocop2/beehest/internal/sandbox"
)

const roundItem = "round/integration/r1"

// gitOut runs git in dir and returns what it printed, without the space at both ends.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // a test runs git on its own temporary folder
	command.Dir = dir

	out, err := command.CombinedOutput()
	require.NoError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

// commitOn makes a commit on the branch of the origin repository and returns its id.
func commitOn(t *testing.T, origin, branch string) string {
	t.Helper()

	if gitOut(t, origin, "branch", "--list", branch) == "" {
		runGit(t, origin, "checkout", "-q", "-b", branch, "main")
	} else {
		runGit(t, origin, "checkout", "-q", branch)
	}

	require.NoError(t, os.WriteFile(filepath.Join(origin, "TIP"), []byte(branch+t.Name()+gitOut(t, origin, "rev-parse", "HEAD")), 0o600))
	runGit(t, origin, "add", "TIP")
	runGit(t, origin, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "tip of "+branch)

	return gitOut(t, origin, "rev-parse", "HEAD")
}

// roundRig is a project whose source is a real repository with a main branch and a round base branch, and the real worktree maker over it.
type roundRig struct {
	origin string
	trees  worktrees
}

func newRoundRig(t *testing.T) roundRig {
	t.Helper()

	origin := repoWithCommit(t)
	commitOn(t, origin, "integration/r1")
	runGit(t, origin, "checkout", "-q", "main")

	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p", Source: origin, BaseBranch: "main"}}}

	return roundRig{origin: origin, trees: newWorktrees(projects, flow.Options{StateDir: evaluated(t, t.TempDir())}, nil, nil)}
}

func (r roundRig) clone() string { return filepath.Join(r.trees.repos, "p") }

func TestWorktrees_EnsureCutsARoundItemDetachedAtTheTipOfItsOwnBase(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	tip := gitOut(t, rig.origin, "rev-parse", "integration/r1")
	require.NotEqual(t, tip, gitOut(t, rig.origin, "rev-parse", "main"), "the base differs from the project base")

	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(rig.trees.root, "p", model.SafeName(roundItem)), dir)
	assert.Equal(t, tip, gitOut(t, dir, "rev-parse", "HEAD"))
	assert.Equal(t, dir, gitOut(t, dir, "rev-parse", "--show-toplevel"))
	assert.Empty(t, gitOut(t, rig.clone(), "branch", "--list", "round/*"), "no branch is made for the round")
	assert.Empty(t, gitOut(t, dir, "branch", "--show-current"), "HEAD is detached")
}

func TestWorktrees_EnsureWithNoStartMovesACleanKeptRoundWorktreeToTheNewTipOfItsBase(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	first := gitOut(t, dir, "rev-parse", "HEAD")
	moved := commitOn(t, rig.origin, "integration/r1")
	require.NotEqual(t, first, moved)

	again, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")

	require.NoError(t, err)
	assert.Equal(t, dir, again)
	assert.Equal(t, moved, gitOut(t, dir, "rev-parse", "HEAD"), "a fresh run starts at the tip of the base")
	assert.Equal(t, "HEAD", gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"), "the tree stays detached")
}

func TestWorktrees_EnsureWithAStartNeverMovesAnExistingRoundWorktreeWhenTheBaseMoves(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	first := gitOut(t, dir, "rev-parse", "HEAD")
	commitOn(t, rig.origin, "integration/r1")

	again, err := rig.trees.Ensure(t.Context(), "p", roundItem, first, "")

	require.NoError(t, err)
	assert.Equal(t, dir, again)
	assert.Equal(t, first, gitOut(t, dir, "rev-parse", "HEAD"), "a later step of the run keeps the commit of the run")
}

func TestWorktrees_EnsureWithNoStartRefusesAKeptRoundWorktreeWithChangesAndNamesItAndHowToRemoveIt(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ name, file string }{
		{"untracked_file", "stray.txt"},
		{"tracked_edit", "TIP"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t)
			dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
			require.NoError(t, err)

			first := gitOut(t, dir, "rev-parse", "HEAD")
			require.NoError(t, os.WriteFile(filepath.Join(dir, test.file), []byte("changed"), 0o600))
			commitOn(t, rig.origin, "integration/r1")

			got, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")

			require.ErrorIs(t, err, errKeptChanges)
			require.ErrorContains(t, err, "the worktree "+dir+" of item "+roundItem+" holds changes")
			require.ErrorContains(t, err, "git -C "+rig.clone()+" worktree remove --force "+dir)
			assert.Empty(t, got)
			assert.Equal(t, first, gitOut(t, dir, "rev-parse", "HEAD"), "the refused tree does not move")
			assert.FileExists(t, filepath.Join(dir, "TIP"), "the refused tree keeps its files")
		})
	}
}

func TestWorktrees_EnsureWithNoStartRefusesAKeptRoundWorktreeWithCommitsOnNoRemoteBranchAndNamesThem(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		commits int
		pushed  bool
	}{
		{"commit_on_no_remote_branch", 1, false},
		{"two_commits_on_no_remote_branch_newest_first", 2, false},
		{"commit_pushed_to_a_branch", 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t)
			dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
			require.NoError(t, err)

			var named []string

			for i := range test.commits {
				file := "work-" + strconv.Itoa(i) + ".txt"
				require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte("work"), 0o600))
				runGit(t, dir, "add", file)
				runGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "local work "+strconv.Itoa(i))
				named = append([]string{gitOut(t, dir, "rev-parse", "--short", "HEAD") + " local work " + strconv.Itoa(i)}, named...)
			}

			local := gitOut(t, dir, "rev-parse", "--short", "HEAD")
			if test.pushed {
				runGit(t, dir, "push", "-q", "origin", "HEAD:refs/heads/saved")
			}

			tip := commitOn(t, rig.origin, "integration/r1")

			got, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")

			if test.pushed {
				require.NoError(t, err)
				assert.Equal(t, tip, gitOut(t, got, "rev-parse", "HEAD"), "a commit that a remote branch holds does not stop the move")

				return
			}

			require.ErrorIs(t, err, errKeptChanges)
			require.ErrorContains(t, err, "the worktree "+dir+" of item "+roundItem+" holds commits that are on no remote branch ("+strings.Join(named, "; ")+")")
			require.ErrorContains(t, err, "git -C "+rig.clone()+" worktree remove --force "+dir)
			assert.Empty(t, got)
			assert.Equal(t, local, gitOut(t, dir, "rev-parse", "--short", "HEAD"), "the refused tree does not move")
		})
	}
}

func TestWorktrees_EnsureWithNoStartMovesAKeptRoundWorktreeAfterAForcePushOfItsBaseAndNamesOnlyTheWorkOfTheItem(t *testing.T) {
	t.Parallel()

	for _, local := range []bool{false, true} {
		t.Run("local_commit="+strconv.FormatBool(local), func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t)
			old := commitOn(t, rig.origin, "integration/r1")

			dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
			require.NoError(t, err)
			require.Equal(t, old, gitOut(t, dir, "rev-parse", "HEAD"))

			named := ""
			if local {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work"), 0o600))
				runGit(t, dir, "add", "work.txt")
				runGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "local work")
				named = gitOut(t, dir, "rev-parse", "--short", "HEAD") + " local work"
			}

			runGit(t, rig.origin, "reset", "-q", "--hard", "HEAD~1")
			runGit(t, rig.origin, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "rewritten base")
			tip := gitOut(t, rig.origin, "rev-parse", "HEAD")
			require.NotEqual(t, old, tip, "the force-push took the old tip off the base")

			got, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")

			if !local {
				require.NoError(t, err, "the old tip of the base is no work of the item")
				assert.Equal(t, tip, gitOut(t, got, "rev-parse", "HEAD"))

				return
			}

			require.ErrorIs(t, err, errKeptChanges)
			require.ErrorContains(t, err, "holds commits that are on no remote branch ("+named+")", "only the commit of the item is named, not the old tip of the base")
		})
	}
}

func TestWorktrees_EnsureWithNoStartKeepsTheTipOfAMoveOutOfTheCommitsOfTheItemWhenTheBaseIsForcePushedAfterwards(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	commitOn(t, rig.origin, "integration/r1")

	_, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	moved := commitOn(t, rig.origin, "integration/r1")
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)
	require.Equal(t, moved, gitOut(t, dir, "rev-parse", "HEAD"), "the first run after the move placed the tree at the new tip")

	runGit(t, rig.origin, "reset", "-q", "--hard", "HEAD~2")
	runGit(t, rig.origin, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "rewritten base")
	tip := gitOut(t, rig.origin, "rev-parse", "HEAD")

	got, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")

	require.NoError(t, err, "the tip that the engine moved the tree to is no work of the item, also after a force-push took it off the base")
	assert.Equal(t, tip, gitOut(t, got, "rev-parse", "HEAD"))
}

func TestWorktrees_EnsureNamesTheGitCallThatFailedWhenItMovesAKeptRoundWorktree(t *testing.T) {
	t.Parallel()

	for _, step := range []string{"status", "fetch", "log", "checkout", "update-ref"} {
		trees, git, _ := ensureFixture(t)

		kept, err := trees.Ensure(t.Context(), "p", roundItem, "", "")
		require.NoError(t, err)

		git.fail = step
		if step == "update-ref" {
			git.failIn = kept
		}

		got, err := trees.Ensure(t.Context(), "p", roundItem, "", "")

		require.ErrorContains(t, err, "git "+step, step)
		assert.Empty(t, got, step)

		if step == "update-ref" {
			require.ErrorContains(t, err, "git update-ref in "+kept+" exited with code 128", "the placed mark of the kept tree fails, not the ref of the clone")
		}
	}
}

func TestWorktrees_EnsureNamesTheGitCallThatFailedWhenItMarksANewRoundWorktree(t *testing.T) {
	t.Parallel()

	trees, git, state := ensureFixture(t)
	tree := filepath.Join(state, "trees", "p", model.SafeName(roundItem))
	git.fail, git.failIn = "update-ref", tree

	got, err := trees.Ensure(t.Context(), "p", roundItem, "", "")

	require.ErrorContains(t, err, "git update-ref in "+tree+" exited with code 128", "the placed mark of the new tree fails, not the ref of the clone")
	assert.Empty(t, got)
}

func TestWorktrees_EnsureRefusesARoundItemWhoseBaseTheRemoteLacksAndNamesTheBase(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", "round/release/none", "", "")

	require.ErrorContains(t, err, `"release/none"`)
	require.ErrorContains(t, err, "Push the base branch to the remote")
	assert.Empty(t, dir)
	assert.NoDirExists(t, filepath.Join(rig.trees.root, "p", model.SafeName("round/release/none")))
}

func TestWorktrees_EnsureCutsAnItemThatIsNotARoundFromTheBaseOfTheProjectOnItsOwnBranch(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "")

	require.NoError(t, err)
	assert.Equal(t, gitOut(t, rig.origin, "rev-parse", "main"), gitOut(t, dir, "rev-parse", "HEAD"))
	assert.Equal(t, "bb-1", gitOut(t, dir, "branch", "--show-current"))
}

func TestWorktrees_EnsureCallsGitInTheOrderThatCutsARoundItemFromItsBase(t *testing.T) {
	t.Parallel()

	trees, git, state := ensureFixture(t)
	clone := filepath.Join(state, "repos", "p")
	want := filepath.Join(state, "trees", "p", model.SafeName(roundItem))

	got, err := trees.Ensure(t.Context(), "p", roundItem, "", "")

	require.NoError(t, err)
	assert.Equal(t, want, got)
	calls := []string{filepath.Join(state, "repos") + ": clone https://example.test/owner/name.git " + clone}
	calls = append(calls, baseFetch(state, "integration/r1")...)
	assert.Equal(t, append(calls,
		clone+": worktree add --force --detach "+want+" origin/integration/r1",
		want+": update-ref "+placedMark+" HEAD",
	), git.calls)
}

func TestWorktrees_EnsureMakesARoundWorktreeAgainThatSomeoneRemovedAtTheRecordedStartCommit(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	start := gitOut(t, dir, "rev-parse", "HEAD")
	require.NoError(t, os.RemoveAll(dir))

	tip := commitOn(t, rig.origin, "integration/r1")
	require.NotEqual(t, start, tip, "the base moved")

	again, err := rig.trees.Ensure(t.Context(), "p", roundItem, start, "")

	require.NoError(t, err)
	assert.Equal(t, dir, again)
	assert.Equal(t, start, gitOut(t, again, "rev-parse", "HEAD"), "the new tree holds the start commit, not the tip of the base")
	assert.Empty(t, gitOut(t, again, "branch", "--show-current"), "HEAD is detached")
}

func TestWorktrees_EnsureMakesARoundWorktreeAgainWithNoFetchSoABaseThatIsGoneDoesNotStopIt(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	start := gitOut(t, dir, "rev-parse", "HEAD")
	require.NoError(t, os.RemoveAll(dir))
	runGit(t, rig.origin, "branch", "-D", "integration/r1")

	again, err := rig.trees.Ensure(t.Context(), "p", roundItem, start, "")

	require.NoError(t, err)
	assert.Equal(t, start, gitOut(t, again, "rev-parse", "HEAD"))
}

func TestWorktrees_EnsureCallsGitInTheOrderThatMakesARoundItemAgainAtItsStart(t *testing.T) {
	t.Parallel()

	trees, git, state := ensureFixture(t)
	clone := filepath.Join(state, "repos", "p")
	want := filepath.Join(state, "trees", "p", model.SafeName(roundItem))

	got, err := trees.Ensure(t.Context(), "p", roundItem, "abc123", "")

	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, []string{
		filepath.Join(state, "repos") + ": clone https://example.test/owner/name.git " + clone,
		clone + ": cat-file -e abc123^{commit}",
		clone + ": worktree add --force --detach " + want + " abc123",
		want + ": update-ref " + placedMark + " HEAD",
	}, git.calls)
}

func TestWorktrees_EnsureStopsWithAPlainErrorWhenTheCloneMadeAgainLacksTheStartCommitOfARoundItem(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	start := gitOut(t, dir, "rev-parse", "HEAD")
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.RemoveAll(rig.clone()))

	// A push rewrites the base, and git prunes the commit that the worktree started at.
	runGit(t, rig.origin, "branch", "-f", "integration/r1", "main")
	runGit(t, rig.origin, "reflog", "expire", "--expire=now", "--all")
	runGit(t, rig.origin, "gc", "-q", "--prune=now")

	again, err := rig.trees.Ensure(t.Context(), "p", roundItem, start, "")

	require.ErrorContains(t, err, "the clone of project p does not hold the commit "+start+" that the worktree of item "+roundItem+" was made at")
	require.ErrorContains(t, err, "End the failed run first: beehest cancel "+roundItem+". Start the item again: beehest run "+roundItem)
	assert.Empty(t, again)
	assert.NoDirExists(t, dir)
}

func TestWorktrees_EnsureMakesARoundWorktreeAgainAtItsStartWhenTheCloneIsGoneAndTheRemoteHoldsTheCommit(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	start := gitOut(t, dir, "rev-parse", "HEAD")
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.RemoveAll(rig.clone()))
	commitOn(t, rig.origin, "integration/r1")

	again, err := rig.trees.Ensure(t.Context(), "p", roundItem, start, "")

	require.NoError(t, err)
	assert.Equal(t, start, gitOut(t, again, "rev-parse", "HEAD"), "the new clone holds the start commit, so the worktree is made there and not at the new tip")
}

func TestWorktrees_EnsureForManyCallersOfARemovedRoundWorktreeAtOnceGivesEveryCallerTheOneFolderAtItsStart(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	start := gitOut(t, dir, "rev-parse", "HEAD")
	require.NoError(t, os.RemoveAll(dir))

	const callers = 4

	dirs, errs := make([]string, callers), make([]error, callers)

	var wg sync.WaitGroup

	for i := range callers {
		wg.Go(func() { dirs[i], errs[i] = rig.trees.Ensure(t.Context(), "p", roundItem, start, "") })
	}

	wg.Wait()

	for i := range callers {
		require.NoError(t, errs[i], "caller %d", i)
		assert.Equal(t, dir, dirs[i], "caller %d", i)
	}

	assert.Equal(t, start, gitOut(t, dir, "rev-parse", "HEAD"))
}

func TestWorktrees_EnsureNamesTheGitCallThatFailedForARoundItem(t *testing.T) {
	t.Parallel()

	for _, step := range []string{"clone", "fetch", "worktree"} {
		trees, _, _ := ensureFixture(t)

		failing := (&fakeGit{fail: step}).runner
		trees.git, trees.remote = failing, failing

		got, err := trees.Ensure(t.Context(), "p", roundItem, "", "")

		require.ErrorContains(t, err, "git "+step)
		assert.Empty(t, got)
	}

	trees, _, _ := ensureFixture(t)
	trees.remote = (&fakeGit{fail: "fetch"}).runner

	_, err := trees.Ensure(t.Context(), "p", roundItem, "", "")

	require.ErrorContains(t, err, `base branch "integration/r1" of round item `+roundItem, "the error names the base")
}

func TestWorktrees_EnsureMakesARemovedRoundWorktreeAgainAndKeepsTheRecordOfARemovedSiblingWorktree(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	sibling, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "")
	require.NoError(t, err)

	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	start := gitOut(t, dir, "rev-parse", "HEAD")
	require.NoError(t, os.RemoveAll(sibling))
	require.NoError(t, os.RemoveAll(dir))

	again, err := rig.trees.Ensure(t.Context(), "p", roundItem, start, "")

	require.NoError(t, err)
	assert.Equal(t, dir, again)
	assert.Contains(t, gitOut(t, rig.clone(), "worktree", "list", "--porcelain"), "worktree "+sibling+"\n",
		"git keeps the record of the removed sibling worktree")
}

func TestWorktrees_EnsureMakesATrackerWorktreeAgainThatSomeoneRemovedOnTheBranchOfTheItem(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "")
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(dir))

	again, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "")

	require.NoError(t, err)
	assert.Equal(t, dir, again)
	assert.Equal(t, model.ItemBranch("bb-1"), gitOut(t, again, "branch", "--show-current"), "the new tree is on the branch of the item")
}

func TestWorktrees_EnsureStillRefusesABranchThatALiveWorktreeOnAnotherPathHolds(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "")
	require.NoError(t, err)

	other := filepath.Join(rig.trees.root, "p", "elsewhere")
	_, err = rig.trees.add(t.Context(), rig.clone(), other, "bb-1", "origin/main")

	require.ErrorContains(t, err, "bb-1")
	assert.NoDirExists(t, other, "no second worktree holds the branch")
	assert.Equal(t, model.ItemBranch("bb-1"), gitOut(t, dir, "branch", "--show-current"))
}

func TestWorktrees_EnsureRefusesATrackerWorktreeWhenGitCannotDropTheRecordOfARemovedOne(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "")
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(dir))

	real := rig.trees.git
	rig.trees.git = func(folder string) proc.Runner { return pruneRefused{Runner: real(folder)} }

	_, err = rig.trees.Ensure(t.Context(), "p", "bb-1", "", "")

	require.ErrorContains(t, err, "fatal: refused")
}

func TestWorktrees_PathOfAnItemNeverFailsWhileSiblingItemsAddTheirWorktrees(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	const (
		siblings = 30
		lookers  = 6
	)

	done := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() {
		defer close(done)

		for i := range siblings {
			_, addErr := rig.trees.Ensure(t.Context(), "p", "bb-"+strconv.Itoa(i), "", "")
			assert.NoError(t, addErr, "sibling %d", i)
		}
	})

	lookups := make([]int, lookers)
	failures := make([]error, lookers)

	for looker := range lookers {
		wg.Go(func() { lookups[looker], failures[looker] = lookUntil(t, rig.trees, done, dir) })
	}

	wg.Wait()

	for looker := range lookers {
		require.NoError(t, failures[looker], "looker %d", looker)
		assert.Positive(t, lookups[looker], "looker %d ran while the siblings added their worktrees", looker)
	}
}

func TestGateSandbox_AGateCopiesTheRoundWorktreeWhileSiblingItemsAddAndLookUpTheirWorktrees(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	box := sandboxFor(t, &fakeEnforcer{}, nil)
	box.clones = cloneLock{repos: rig.trees.repos}

	done := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() {
		defer close(done)

		for i := range 30 {
			_, addErr := rig.trees.Ensure(t.Context(), "p", "bb-"+strconv.Itoa(i), "", "")
			assert.NoError(t, addErr, "sibling %d", i)
		}
	})

	const gaters = 6

	gates := make([]int, gaters)

	lookups := make([]int, gaters)
	failures := make([]error, gaters)

	for gater := range gaters {
		wg.Go(func() { gates[gater] = gateUntil(t, box, done, dir) })
		wg.Go(func() { lookups[gater], failures[gater] = lookUntil(t, rig.trees, done, dir) })
	}

	wg.Wait()

	for gater := range gaters {
		assert.Positive(t, gates[gater], "gater %d ran while the siblings added their worktrees", gater)
		require.NoError(t, failures[gater], "looker %d", gater)
		assert.Positive(t, lookups[gater], "looker %d ran while the gates copied the worktree", gater)
	}
}

func TestWorktrees_WithTreeRunsFnWhileItHoldsTheLockOfTheClone(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	dir, err := rig.trees.Ensure(t.Context(), "p", roundItem, "", "")
	require.NoError(t, err)

	clones := cloneLock{repos: rig.trees.repos}
	free := func(string) error { return nil }

	var got string

	var inside error

	require.NoError(t, rig.trees.WithTree(t.Context(), "p", roundItem, func(found string) {
		got = found

		short, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()

		inside = clones.hold(short, "p", free)
	}))

	assert.Equal(t, dir, got)
	require.ErrorIs(t, inside, context.DeadlineExceeded, "a second holder took the lock of the clone while fn ran")
	require.NoError(t, clones.hold(t.Context(), "p", free), "the lock is free once WithTree returns")
}

// gateUntil runs a sandboxed gate in dir until done closes or a gate fails. It returns the count of gates that succeeded.
func gateUntil(t *testing.T, box gateSandbox, done <-chan struct{}, dir string) int {
	t.Helper()

	for count := 0; ; count++ {
		select {
		case <-done:
			return count
		default:
		}

		fake := &fakeEnforcer{}
		box.enforcer = func(string) (sandbox.Enforcer, error) { return fake, nil }

		if out, code := box.run(t.Context(), "p", dir, []string{"true"}, nil, 0); !assert.Equal(t, 0, code, "gate %d: %s", count, out) {
			return count
		}
	}
}

// lookUntil looks up the round worktree until done closes. It returns the count of lookups and the first failure.
func lookUntil(t *testing.T, trees worktrees, done <-chan struct{}, dir string) (int, error) {
	t.Helper()

	for count := 0; ; count++ {
		select {
		case <-done:
			return count, nil
		default:
		}

		got, err := trees.Path(t.Context(), "p", roundItem)
		if err != nil {
			return count, err
		}

		if got != dir {
			return count, fmt.Errorf("lookup %d found %q, want %q", count, got, dir)
		}
	}
}

func TestWorktrees_EnsureCutsAChildItemOfARoundFromTheBaseOfTheRoundOnItsOwnBranch(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)
	tip := gitOut(t, rig.origin, "rev-parse", "integration/r1")
	require.NotEqual(t, tip, gitOut(t, rig.origin, "rev-parse", "main"), "the base of the round differs from the project base")

	dir, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "integration/r1")

	require.NoError(t, err)
	assert.Equal(t, tip, gitOut(t, dir, "rev-parse", "HEAD"))
	assert.Equal(t, "bb-1", gitOut(t, dir, "branch", "--show-current"))
}

func TestWorktrees_EnsureRefusesAChildItemWhoseRoundBaseTheRemoteLacksAndNamesTheBase(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t)

	dir, err := rig.trees.Ensure(t.Context(), "p", "bb-1", "", "integration/none")

	require.ErrorContains(t, err, "integration/none")
	assert.Empty(t, dir)
}
