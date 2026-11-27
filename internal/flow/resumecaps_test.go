package flow

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// capFile writes a flow and its prompt file and returns the flow path.
func capFile(t *testing.T, flow string) string {
	t.Helper()

	file := writeFlow(t, flow)
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("x\n"), 0o600))

	return file
}

func TestResume_TheCostSumOfAnItemSurvivesARestartSoAStepPastTheCapWaitsAgain(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	rig.reply("", costing(3))

	first := holdOnCap(t, rig, capFile(t, threeFlow))
	require.Len(t, rig.h.agents.seen(), 2, "a and b ran, c waits at the cap")

	looks := func(engine *Engine) int {
		steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
		require.NoError(t, err)

		count := 0

		for _, step := range steps {
			if strings.Contains(step.StepName, "/budget1.") {
				count++
			}
		}

		return count
	}

	second := rig.h.engine(t)
	seen := looks(second)

	// The recovered run replays the recorded looks and reads the cap again: the sum of a and b is not reset, so c waits once more.
	waitOn(t, waiting[struct{}]{what: "the recovered run to look at the cap again", run: runOfEngine(t, second), read: func() (struct{}, bool, error) { return struct{}{}, looks(second) > seen, nil }})

	stop := stoppedRun(t, rig.h.client(t, testVersion), testRun)

	require.NotNil(t, stop)
	assert.InDelta(t, first.Spent, stop.Spent, 0, "the replay reads the recorded costs of a and b")
	assert.Len(t, rig.h.agents.seen(), 2, "no agent started after the restart: c waits again")
}

func TestResume_AStepThatARaisedCapLetsRunShowsNoStopWhileItsAgentRuns(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5

	var calls atomic.Int32

	release := make(chan struct{})

	// The third call is c, after the raise: its agent runs until the test reads the view.
	rig.h.agents.result = func(AgentRequest) agent.Result {
		if calls.Add(1) >= 3 {
			hang(release)
		}

		return costing(3)
	}

	holdOnCap(t, rig, capFile(t, threeFlow))

	rig.h.opts.MaxRunBudget = 100
	engine := rig.h.engine(t)
	t.Cleanup(func() { close(release) })

	waitOn(t, conditionOf(runOfEngine(t, engine), func() bool { return calls.Load() == 3 }))

	assert.Nil(t, stoppedRun(t, rig.h.client(t, testVersion), testRun), "the look of the phase that runs the agent of c passed")

	records, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	phase := records[len(records)-1].ChildWorkflowID
	require.NotEmpty(t, phase, "the last record of the run is the phase of c")

	failing := &Client{flow: &stepsOfOneFail{Context: engine.ctx, id: phase}}
	_, err = failing.Steps(testRun)
	require.ErrorContains(t, err, "read the look at the caps of phase "+phase)
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

	_, err := engine.RunFile(testItem, testProject, file, modeOff())
	require.ErrorContains(t, err, "ended as exited (exit 3)")
	require.Len(t, rig.h.agents.seen(), 2, "round 1 ran, round 2 failed")

	_, err = resumeAndJoin(t, engine, client)

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 4, "round 1 is replayed and not run again: round 2 again, then round 3. The loop of 3 rounds does not start over")
}
