package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

// costing is a finished run that cost the given dollars.
func costing(dollars float64) agent.Result {
	done := exited
	done.Cost = dollars

	return done
}

var costFlow = bodyFlow(`a = agent(model = "m", prompt = "prompts/a.md", name = "a")
command(run = ["show", str(a.cost_usd)])
`)

func TestAgentStep_TheCostOfTheRunIsTheOutputCostUSD(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", costing(1.25))

	_, err := rig.run(t, costFlow, "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show 1.25"}, rig.h.commands.ran())
}

func TestAgentStep_ARunWithNoReportedCostHasTheCostZero(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, costFlow, "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show 0"}, rig.h.commands.ran())
}

func TestAgentStep_TheCapOfTheStepGoesToTheAgentRequest(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		extra string
		want  float64
	}{
		{`max_budget_usd = 2.5, `, 2.5},
		{`max_budget_usd = 3, `, 3},
		{``, 0},
	} {
		rig := newAgentRig(t)
		rig.reply("", exited)

		_, err := rig.run(t, agentFlow(tc.extra), "x")

		require.NoError(t, err, tc.extra)
		require.Len(t, rig.h.agents.seen(), 1)
		assert.InDelta(t, tc.want, rig.h.agents.seen()[0].MaxBudget, 0, tc.extra)
	}
}

func TestAgentStep_ABadStepCapFailsTheStepAndNamesTheKeyword(t *testing.T) {
	t.Parallel()

	for _, extra := range []string{`max_budget_usd = -1, `, `max_budget_usd = "5", `, `max_budget_usd = float("nan"), `, `max_budget_usd = float("inf"), `} {
		rig := newAgentRig(t)
		rig.reply("", exited)

		_, err := rig.run(t, agentFlow(extra), "x")

		require.Error(t, err, extra)
		assert.Contains(t, err.Error(), "max_budget_usd", extra)
		assert.Empty(t, rig.h.agents.seen(), extra)
	}
}

var threeFlow = bodyFlow(`a = agent(model = "m", prompt = "prompts/a.md", name = "a")
b = agent(model = "m", prompt = "prompts/a.md", name = "b")
c = agent(model = "m", prompt = "prompts/a.md", name = "c")
command(run = ["done"])
`)

func TestAgentStep_AStepThatWouldStartPastTheRunCapFailsBeforeItStartsAndNamesTheCapAndTheSpentSum(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	rig.reply("", costing(3))

	_, err := rig.run(t, threeFlow, "x")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the cap for one item run is $5")
	assert.Contains(t, err.Error(), "the agent steps of the item run spent $6")
	assert.Contains(t, err.Error(), "the last step can pass it by its own cost")
	assert.Len(t, rig.h.agents.seen(), 2, "the third agent never started")
	assert.Empty(t, rig.h.commands.ran())
}

func TestAgentStep_AStepStartsWhileTheSumIsUnderTheCapAndIsRefusedWhenTheSumEqualsTheCap(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	rig.reply("", costing(2.5))

	_, err := rig.run(t, threeFlow, "x")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the agent steps of the item run spent $5")
	assert.Len(t, rig.h.agents.seen(), 2, "the second step started at $2.5 of $5, the third was refused at $5 of $5")
	assert.Empty(t, rig.h.commands.ran())
}

func TestAgentStep_TheCostOfAFailedRunIsRecordedForTheRunCap(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	failed := costing(6)
	failed.ExitCode = 3
	failed.Note = "bad exit"
	rig.reply("", failed)

	file := writeFlow(t, threeFlow)
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("x\n"), 0o600))

	engine := rig.h.engine(t)
	_, err := engine.RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "ended as exited (exit 3)", "a failed run still fails the step with its own error")

	steps, err := dbos.GetWorkflowSteps(engine.ctx, runID(testItem, 1), dbos.WithStepsLoadOutput(true))
	require.NoError(t, err)

	var recorded string

	for _, step := range steps {
		if strings.HasSuffix(step.StepName, "/spent1") {
			recorded, _ = step.Output.(string) //nolint:errcheck // a missing output reads as empty and fails the assertion
		}
	}

	assert.Contains(t, recorded, `"cost_usd":6`, "the run cap counts the 6 dollars that the failed run spent")
}

func TestAgentStep_ARunWithNoRunCapStartsEveryStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", costing(100))

	_, err := rig.run(t, threeFlow, "x")

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 3)
}

func TestAgentStep_AReplayCountsTheRecordedCostsNotTheCostOfANewRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	rig.reply("", costing(3))

	file := writeFlow(t, bodyFlow(`a = agent(model = "m", prompt = "prompts/a.md", name = "a")
ask = wait("go?", name = "ask")
b = agent(model = "m", prompt = "prompts/a.md", name = "b")
c = agent(model = "m", prompt = "prompts/a.md", name = "c")
command(run = ["done"])
`))
	prompt := filepath.Join(filepath.Dir(file), "prompts", "a.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(prompt), 0o750))
	require.NoError(t, os.WriteFile(prompt, []byte("x\n"), 0o600))

	interrupted(t, rig.h, file)
	require.Len(t, rig.h.agents.seen(), 1)

	// A new run of step a would cost 10 and refuse step b. The replay reads the 3 that the first run recorded, so b starts.
	rig.reply("", costing(10))

	next := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "yes") == nil })

	outcome := <-done

	require.Error(t, outcome.err)
	assert.Contains(t, outcome.err.Error(), "the agent steps of the item run spent $13", "3 from the record of a, then 10 from b")
	assert.Len(t, rig.h.agents.seen(), 2, "a ran once, b ran once, c never started")
}
