package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

const papersScope = `allow_paths = ["papers/**"], `

func TestAgentStep_AllowPathsNamesAnEditOfAFileInsideAnIgnoredFolderThatWasThereBefore(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "build/cache.o"), "a longer body than before")
		rig.put(filepath.Join(rig.dir, "papers/new.md"), "x")

		return exited
	}

	_, err := rig.run(t, agentFlow(papersScope), "x")

	require.ErrorContains(t, err, "build/cache.o")
	assert.NotContains(t, err.Error(), "run.log")
}

func TestAgentStep_AllowPathsAcceptsAWriteInsideAKnownBuildCacheThatWasThereBefore(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)
	rig.put(filepath.Join(rig.dir, ".git/info/exclude"), "node_modules/\n")
	rig.put(filepath.Join(rig.dir, "node_modules/pkg/a.js"), "x")
	rig.writesIn("papers/new.md", "node_modules/pkg/b.js", "node_modules/other/c.js")

	_, err := rig.run(t, agentFlow(papersScope), "x")

	require.NoError(t, err)
}

func TestAgentStep_AllowPathsNamesAKnownBuildCacheFolderThatTheAgentCreated(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)
	rig.put(filepath.Join(rig.dir, ".git/info/exclude"), "node_modules/\n")
	rig.writesIn("papers/new.md", "node_modules/pkg/b.js")

	_, err := rig.run(t, agentFlow(papersScope), "x")

	require.ErrorContains(t, err, "node_modules/")
	assert.NotContains(t, err.Error(), "b.js")
}

func TestAgentStep_AllowPathsNamesAFileThatTheAgentWroteBesideTheItemFolder(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)
	rig.put(filepath.Join(filepath.Dir(rig.dir), "other-checkout/old.txt"), "x")
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "papers/new.md"), "x")
		rig.put(filepath.Join(filepath.Dir(rig.dir), "other-checkout/build.sh"), "x")
		rig.put(filepath.Join(filepath.Dir(rig.dir), "elsewhere/y.txt"), "x")

		return exited
	}

	_, err := rig.run(t, agentFlow(papersScope), "x")

	require.ErrorContains(t, err, "../other-checkout/build.sh")
	require.ErrorContains(t, err, "../elsewhere/y.txt")
	assert.NotContains(t, err.Error(), "old.txt")
}

func TestAgentStep_AllowPathsLeavesTheWorktreeOfAnotherItemAlone(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	sibling := filepath.Join(filepath.Dir(rig.dir), "sibling")
	gitIn(t, rig.dir, "worktree", "add", "-q", "--detach", sibling)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "papers/new.md"), "x")
		rig.put(filepath.Join(sibling, "src/other-item.go"), "x")

		return exited
	}

	_, err := rig.run(t, agentFlow(papersScope), "x")

	require.NoError(t, err)
}

func TestScope_AMarkRecordedBeforeTheFilesBesideTheFolderWereReadChecksNothingBeside(t *testing.T) {
	t.Parallel()

	dir, _ := gitTree(t)
	start := markOf(t, dir)
	putUnder(t, filepath.Dir(dir), "other-checkout/x.sh", "x")

	bad, err := scopeOf(t, "papers/**").outside(t.Context(), engineGit(dir), start)

	require.NoError(t, err)
	assert.Equal(t, []string{"../other-checkout/x.sh"}, bad)

	start.Outside = nil
	bad, err = scopeOf(t, "papers/**").outside(t.Context(), engineGit(dir), start)

	require.NoError(t, err)
	assert.Empty(t, bad)
}

func TestScope_AFolderBesideTheItemFolderThatCannotBeReadIsSkipped(t *testing.T) {
	t.Parallel()

	dir, _ := gitTree(t)
	locked := filepath.Join(filepath.Dir(dir), "locked")
	putUnder(t, locked, "a.txt", "x")
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	state, err := readState(t.Context(), engineGit(dir))

	require.NoError(t, err)
	assert.Empty(t, state.outside)
}

func TestScope_AGitCallThatFailsWhileListingTheWorktreesNamesTheCall(t *testing.T) {
	t.Parallel()

	dir, _ := gitTree(t)
	inner := engineGit(dir)

	_, err := readState(t.Context(), failing(func(args []string) model.CommandResult { return inner.Run(t.Context(), args) }, "worktree"))

	require.ErrorContains(t, err, "list the worktrees of the repository with git worktree")
}

func TestScope_OnlyAFileInsideAKnownBuildCacheFolderIsOneEntryOfTheFolder(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		"node_modules/a/b.js":       "node_modules/",
		"pkg/__pycache__/x.pyc":     "pkg/__pycache__/",
		"a/.cache/b/c":              "a/.cache/",
		"build/o":                   "build/o",
		"a.log":                     "a.log",
		"docs/node_modules":         "docs/node_modules",
		"node_modules_extra/x.js":   "node_modules_extra/x.js",
		"src/node_modules/x/.cache": "src/node_modules/",
	} {
		assert.Equal(t, want, collapse(name), name)
	}
}
