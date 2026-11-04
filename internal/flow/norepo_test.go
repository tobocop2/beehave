package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// plainRig is an agent rig whose item folder is a plain folder with no git repository, for a project with no repository.
func plainRig(t *testing.T) *agentRig {
	t.Helper()

	dir := t.TempDir()
	h := newHarness(t)
	h.trees = &fakeTrees{dir: dir}
	h.repository = func(string) bool { return false }

	return &agentRig{h: h, dir: dir}
}

func TestAgentStep_AProjectWithNoRepositoryRunsTheAgentInAPlainFolderAndRewroteIsFalse(t *testing.T) {
	t.Parallel()

	rig := plainRig(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "notes.md"), "found")

		return exited
	}

	_, err := rig.run(t, rewroteFlow(), "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show False"}, rig.h.commands.ran())
	assert.NoDirExists(t, filepath.Join(rig.dir, ".git"))
}

func TestAgentStep_AProjectWithNoRepositoryRefusesAPinAndAnAllowPathsScope(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{"pin": `pin = "0123456789abcdef", `, "allow_paths": `allow_paths = ["notes/"], `} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := plainRig(t)

			_, err := rig.run(t, bodyFlow("agent_result = "+agentCallText(call)+"\n"), "x")

			require.Error(t, err)
			assert.Contains(t, err.Error(), "no repository")
			_, statErr := os.Stat(filepath.Join(rig.dir, ".git"))
			assert.True(t, os.IsNotExist(statErr))
		})
	}
}

func TestRemoveTree_KeepsThePlainFolderOfAProjectWithNoRepositoryAndRunsNoGit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.md"), []byte("kept"), 0o600))

	engine := &Engine{deps: Deps{Trees: fakeTrees{dir: dir}, Repository: func(string) bool { return false }}}

	kept, err := engine.removeTree(t.Context(), testProject, testItem)

	require.NoError(t, err)
	assert.Contains(t, kept, "no repository")
	assert.FileExists(t, filepath.Join(dir, "notes.md"))
}

func TestBuiltins_AGateThatNamesItsOwnProgramRunsInAProjectWithNoRepositoryAndRecordsAnEmptyHead(t *testing.T) {
	t.Parallel()

	rig := plainRig(t)

	_, err := rig.run(t, bodyFlow("c = gate(run = [\"true\"], name = \"check\")\ncommand(run = [\"show\", \"[\" + c.head + \"][\" + c.tree + \"]\"])\n"), "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"true", "show [][]"}, rig.h.commands.ran())
}

func TestBuiltins_AGateWhoseRunHoldsNoProgramIsRefusedInAProjectWithNoRepository(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"empty list":     `gate(run = [], name = "check")`,
		"none":           `gate(run = None, name = "check")`,
		"computed empty": "program = []\ngate(run = program, name = \"check\")",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := plainRig(t)

			_, err := rig.run(t, bodyFlow(call+"\n"), "x")

			require.Error(t, err)
			assert.Contains(t, err.Error(), "no repository")
			assert.Empty(t, rig.h.commands.ran())
		})
	}
}

func TestBuiltins_AProjectWithNoRepositoryRefusesTheGitBuiltinsWithAPlainMessage(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"gate":        `gate(name = "check")`,
		"merge":       `merge(head = "abc", name = "land")`,
		"batch_merge": `batch_merge(prs = [struct(item = "1", head = "abc")], base = "main", repo = "o/n", name = "batch")`,
		"preflight":   `preflight(head = "abc", base = "main", name = "look")`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := plainRig(t)

			_, err := rig.run(t, bodyFlow(call+"\n"), "x")

			require.Error(t, err)
			assert.Contains(t, err.Error(), name)
			assert.Contains(t, err.Error(), "no repository")
			assert.Empty(t, rig.h.commands.ran())
		})
	}
}
