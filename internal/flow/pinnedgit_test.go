package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/exectest"
)

// The agent writes core.fsmonitor into the config of the item tree before it ends. The reads of the engine after the agent (the status, the diff, the
// list of the files and the ref of the start) start no program that the config names: the engine refuses the tree and names the setting.
func TestAgentStep_EngineReadsStartNoProgramInTheItemTree(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	ran := filepath.Join(t.TempDir(), "ran")
	marker := filepath.Join(t.TempDir(), "marker")
	require.NoError(t, exectest.Write(marker, "#!/bin/sh\ntouch "+ran+"\nexit 1\n"))

	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "papers", "a.md"), "x")
		gitIn(t, rig.dir, "config", "core.fsmonitor", marker)

		return exited
	}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, flowFile(t, agentFlow(`allow_paths = ["papers/**"], `), "x"), modeOff())

	require.ErrorContains(t, err, "holds core.fsmonitor, which beehave does not allow")
	assert.NoFileExists(t, ran, "a read of the engine in the item tree ran the program that the tree config names")
}

// The item worktree holds a commit that is not on the remote, and its .git file names a worktree of another clone at the pushed tip of the item. The
// release reads the tree that the clone of the project holds and keeps it for the commit; the git file does not choose the repository to read or to
// remove from.
func TestRelease_ARewrittenGitFileDoesNotChooseTheRepositoryToRemoveFrom(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	commitIn(t, r.tree, "unpushed")

	decoy := filepath.Join(t.TempDir(), "decoy")
	gitIn(t, filepath.Dir(decoy), "clone", "-q", r.remote, decoy)

	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, decoy, "worktree", "add", "--detach", other, "origin/item-12")

	pointer, err := os.ReadFile(filepath.Join(other, ".git")) //nolint:gosec // a file of the test
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(r.tree, ".git"), pointer, 0o600)) //nolint:gosec // a file of the test

	kept, err := removeTreeOf(t, r.tree, 0)

	require.NoError(t, err)
	assert.Contains(t, kept, "not the tip of a branch on the remote", "the release reads HEAD of the tree that the clone holds")
	assert.DirExists(t, r.tree)
	assert.Contains(t, gitIn(t, decoy, "worktree", "list"), other, "the other clone keeps its worktree")
}
