package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

// rewroteFlow is an agent call and a command that shows the output rewrote.
func rewroteFlow() string {
	return bodyFlow("a = " + agentCallText("") + `command(run = ["show", str(a.rewrote)])` + "\n")
}

// rewroteRetryFlow is an agent call with two tries and a declared output, and a command that shows the output rewrote.
func rewroteRetryFlow() string {
	return bodyFlow("a = " + agentCallText(`outputs = ["verdict"], output_tries = 2, `) + `command(run = ["show", str(a.rewrote)])` + "\n")
}

func TestAgentStep_RewroteIsTrueWhenTheAgentAmendsTheCommitItStartedOn(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "x.txt"), "x")
		gitIn(t, rig.dir, "add", "x.txt")
		gitIn(t, rig.dir, "commit", "-q", "--amend", "-m", "again")

		return exited
	}

	_, err := rig.run(t, rewroteFlow(), "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show True"}, rig.h.commands.ran())
}

func TestRewroteHistory_AStartCommitThatGitCannotFindIsAnErrorNotARewrite(t *testing.T) {
	t.Parallel()

	dir, head := gitTree(t)

	rewrote, err := rewroteHistory(t.Context(), dir, "0123456789012345678901234567890123456789", head)

	require.ErrorContains(t, err, "compare the start of the agent")
	assert.False(t, rewrote)
}

func TestAgentStep_RewroteIsFalseWhenTheAgentOnlyAddsACommit(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "x.txt"), "x")
		gitIn(t, rig.dir, "add", "x.txt")
		gitIn(t, rig.dir, "commit", "-q", "-m", "more")

		return exited
	}

	_, err := rig.run(t, rewroteFlow(), "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show False"}, rig.h.commands.ran())
}

func TestAgentStep_RewroteIsTrueWhenTheAgentResetsToAnEarlierCommit(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	gitIn(t, rig.dir, "commit", "-q", "--allow-empty", "-m", "two")
	rig.h.agents.result = func(AgentRequest) agent.Result {
		gitIn(t, rig.dir, "reset", "-q", "--hard", "HEAD~1")

		return exited
	}

	_, err := rig.run(t, rewroteFlow(), "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show True"}, rig.h.commands.ran())
}

func TestAgentStep_RewroteIsTrueWhenTheAgentMovesTheItemOntoAnotherLine(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	gitIn(t, rig.dir, "commit", "-q", "--allow-empty", "-m", "two")
	rig.h.agents.result = func(AgentRequest) agent.Result {
		gitIn(t, rig.dir, "reset", "-q", "--hard", "HEAD~1")
		gitIn(t, rig.dir, "commit", "-q", "--allow-empty", "-m", "other")

		return exited
	}

	_, err := rig.run(t, rewroteFlow(), "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show True"}, rig.h.commands.ran())
}

// The baseline is the commit before the agent call, not before each try: the history that the flag protects is the history that existed before the step.
func TestAgentStep_AnAmendOfACommitFromAnEarlierTryOfTheSameCallIsNotARewrite(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	calls := 0
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		calls++

		if calls == 1 {
			gitIn(t, rig.dir, "commit", "-q", "--allow-empty", "-m", "try one")

			return exited
		}

		gitIn(t, rig.dir, "commit", "-q", "--allow-empty", "--amend", "-m", "try two")
		rig.put(outputPath(request.Prompt), `{"verdict":"ok"}`)

		return exited
	}

	_, err := rig.run(t, rewroteRetryFlow(), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 2)
	assert.Equal(t, []string{"show False"}, rig.h.commands.ran())
}

func TestAgentStep_AnAmendOfACommitFromBeforeTheStepIsARewriteOnALaterTry(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	calls := 0
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		calls++

		if calls == 1 {
			return exited
		}

		gitIn(t, rig.dir, "commit", "-q", "--allow-empty", "--amend", "-m", "try two")
		rig.put(outputPath(request.Prompt), `{"verdict":"ok"}`)

		return exited
	}

	_, err := rig.run(t, rewroteRetryFlow(), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 2)
	assert.Equal(t, []string{"show True"}, rig.h.commands.ran())
}

func TestAgentStep_AReplayKeepsTheStartThatTheRenderStepRecorded(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		gitIn(t, rig.dir, "commit", "-q", "--allow-empty", "--amend", "-m", "again")

		return exited
	}

	file := writeFlow(t, rewroteFlow())
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("x"), 0o600))

	rig.crashAfterThePhaseEnded(t, file)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 1, "the agent does not run again")
	assert.Equal(t, []string{"show True"}, rig.h.commands.ran(), "the replay compares with the start that was recorded before the agent")
}

func TestAgentRead_ARecordFromBeforeRewroteWasKeptDecodesWithRewroteFalse(t *testing.T) {
	t.Parallel()

	old := `{"text":"t","session":"s","transcript":"x","head_sha":"abc","violations":"","stop":"","cost_usd":1.5,"verdict":"PASS"}`

	var read agentRead

	require.NoError(t, json.Unmarshal([]byte(old), &read))
	assert.False(t, read.Rewrote)
	assert.Equal(t, "abc", read.Head)

	var line StepLine
	line.fromAgentRead(read)
	assert.False(t, line.Rewrote)
	assert.Equal(t, "abc", line.Head)
}

func TestAgentArgs_ARenderRecordWithoutStartHeadRecallsEmptyAndNeverRewrites(t *testing.T) {
	t.Parallel()

	a := agentArgs{file: t.TempDir() + "/out/x.json"}
	require.NoError(t, a.recall(map[string]any{recordedOutputFile: a.file}))
	assert.Empty(t, a.startHead)

	rewrote, err := rewroteHistory(t.Context(), t.TempDir(), a.startHead, "deadbeef")
	require.NoError(t, err)
	assert.False(t, rewrote)
}
