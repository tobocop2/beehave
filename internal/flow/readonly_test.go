package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

// readOnlyFlow is a read-only agent call and a command that shows its violations.
func readOnlyFlow(edits string) string {
	return bodyFlow("a = " + agentCallText(edits) + `command(run = ["show", a.violations])` + "\n")
}

// trackedRig is an agent rig whose folder holds one tracked file, committed before the run.
func trackedRig(t *testing.T) (*agentRig, string) {
	t.Helper()

	rig := newAgentRig(t)
	file := filepath.Join(rig.dir, "a.txt")

	rig.put(file, "one")
	gitIn(t, rig.dir, "add", "a.txt")
	gitIn(t, rig.dir, "commit", "-q", "-m", "tracked")

	return rig, file
}

func TestAgentStep_AReadOnlyCallThatChangedATrackedFileHasAViolation(t *testing.T) {
	t.Parallel()

	rig, file := trackedRig(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(file, "two")

		return exited
	}

	_, err := rig.run(t, readOnlyFlow("edits = False, "), "x")

	require.NoError(t, err)
	require.Len(t, rig.h.commands.ran(), 1)
	assert.Contains(t, rig.h.commands.ran()[0], "changed the item folder")
}

func TestAgentStep_AReadOnlyCallThatCommittedHasAViolation(t *testing.T) {
	t.Parallel()

	rig, _ := trackedRig(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		gitIn(t, rig.dir, "commit", "-q", "--allow-empty", "-m", "more")

		return exited
	}

	_, err := rig.run(t, readOnlyFlow("edits = False, "), "x")

	require.NoError(t, err)
	require.Len(t, rig.h.commands.ran(), 1)
	assert.Contains(t, rig.h.commands.ran()[0], "changed the item folder")
}

func TestAgentStep_AReadOnlyCallThatOnlyWroteAnUntrackedFileHasNoViolation(t *testing.T) {
	t.Parallel()

	rig, _ := trackedRig(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "table.md"), "rows")

		return exited
	}

	_, err := rig.run(t, readOnlyFlow("edits = False, "), "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show "}, rig.h.commands.ran())
}

func TestAgentStep_AReadOnlyCallInAFolderWhoseIndexIsBrokenIsAnError(t *testing.T) {
	t.Parallel()

	dir, head := gitTree(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("junk"), 0o600))

	a := agentArgs{dir: dir, startHead: head}

	changed, err := a.changedReadOnly(t.Context(), head)
	require.ErrorContains(t, err, "read the tracked changes of the item folder")
	assert.False(t, changed)

	_, _, _, err = a.endState(t.Context(), nil)
	require.ErrorContains(t, err, "read the tracked changes of the item folder")
}

func TestAgentStep_ACallThatMayEditIsNotCheckedForChangedFiles(t *testing.T) {
	t.Parallel()

	rig, file := trackedRig(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(file, "two")

		return exited
	}

	_, err := rig.run(t, readOnlyFlow(""), "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show "}, rig.h.commands.ran())
}

func TestExample_AnApproveFromAReviewThatChangedTheFolderIsNotClean(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	inner := rig.h.agents.result
	reviews := 0

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)

		switch {
		case strings.Contains(request.Prompt, reviewMarker):
			reviews++

			if reviews == 1 {
				rig.put(filepath.Join(rig.dir, "edited.txt"), "a shell write of the review")
				gitIn(t, rig.dir, "add", "edited.txt")
			}
		case request.Resume != "":
			gitIn(t, rig.dir, "rm", "-qf", "edited.txt")
		}

		return result
	}

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus"}, rig.models(), "review 1 approved but changed the folder, so a fix ran and review 2 approved")
	assert.Equal(t, "merged", result.Merge)
}
