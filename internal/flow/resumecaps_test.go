package flow

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// capFile writes a flow and its prompt file and returns the flow path.
func capFile(t *testing.T, flow string) string {
	t.Helper()

	file := writeFlow(t, flow)
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("x\n"), 0o600))

	return file
}

func TestResume_TheCostSumOfAnItemSurvivesTheResumeSoAStepPastTheCapIsRefusedAgain(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	rig.reply("", costing(3))

	file := capFile(t, threeFlow)
	engine := rig.h.engine(t)
	client := rig.h.client(t, testVersion)

	_, err := engine.RunFile(testItem, testProject, file)
	require.ErrorContains(t, err, "the agent steps of the item run spent $6")
	require.Len(t, rig.h.agents.seen(), 2, "a and b ran, c was refused at the cap")

	_, err = resumeAndJoin(t, engine, client)

	require.ErrorContains(t, err, "the agent steps of the item run spent $6", "the resume replays the recorded costs of a and b: the sum is not reset")
	assert.Len(t, rig.h.agents.seen(), 2, "no agent started after the resume: c is refused again")
}

func TestResume_TheRoundsOfALoopAreNotCountedAgainAfterTheResume(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)

	var calls atomic.Int32

	// The second agent call of the run fails once. A resume runs that step again and then the third round.
	rig.h.agents.result = func(AgentRequest) agent.Result {
		failed := exited
		if calls.Add(1) == 2 {
			failed.ExitCode = 3
			failed.Note = "bad exit"
		}

		return failed
	}

	file := capFile(t, bodyFlow(`for round in range(1, 4):
    agent(model = "m", prompt = "prompts/a.md", name = "r%d" % round)
command(run = ["done"])
`))
	engine := rig.h.engine(t)
	client := rig.h.client(t, testVersion)

	_, err := engine.RunFile(testItem, testProject, file)
	require.ErrorContains(t, err, "ended as exited (exit 3)")
	require.Len(t, rig.h.agents.seen(), 2, "round 1 ran, round 2 failed")

	_, err = resumeAndJoin(t, engine, client)

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 4, "round 1 is replayed and not run again: round 2 again, then round 3. The loop of 3 rounds does not start over")
}
