package flow

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

// estimated is a finished run whose cost obee estimated from the tokens.
func estimated(dollars float64) agent.Result {
	done := costing(dollars)
	done.Estimated = true

	return done
}

// unpriced is a finished run that used tokens of a model with no price: its cost is unknown.
func unpriced() agent.Result {
	done := exited
	done.Unknown = true

	return done
}

func TestStatus_AnEstimatedCostIsMarkedOnTheStepAndInTheSumOfTheRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 20

	var calls atomic.Int32

	rig.h.agents.result = func(AgentRequest) agent.Result {
		if calls.Add(1) == 1 {
			return costing(1.25)
		}

		return estimated(0.5)
	}

	_, err := rig.run(t, twoAgentsFlow, "x")
	require.NoError(t, err)

	view, err := rig.h.client(t, testVersion).Steps(testRun)
	require.NoError(t, err)

	var marked []bool

	for _, line := range view.Steps {
		if line.Cost > 0 {
			marked = append(marked, line.Estimated)
		}
	}

	assert.Equal(t, []bool{false, true}, marked, "only the step whose cost obee estimated carries the mark")
	assert.InDelta(t, 1.75, view.Spent.Cost, 0)
	assert.True(t, view.Spent.Estimated, "a sum that holds an estimate says so")
	assert.Contains(t, view.Notes(), "spent $1.75 (estimate) of $20")
}

func TestStatus_TheCostOfAFailedRunKeepsItsEstimateMark(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	failed := estimated(0.75)
	failed.ExitCode = 1
	rig.reply("", failed)

	_, err := rig.run(t, twoAgentsFlow, "x")
	require.Error(t, err)

	info := onlyRun(t, rig.h.client(t, testVersion))

	assert.InDelta(t, 0.75, info.Spent.Cost, 0)
	assert.True(t, info.Spent.Estimated)
	assert.Contains(t, info.Notes(), "spent $0.75 (estimate)")
}

func TestStatus_ACostThatIsUnknownIsNotShownAsZeroDollars(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", unpriced())

	_, err := rig.run(t, twoAgentsFlow, "x")
	require.NoError(t, err)

	info := onlyRun(t, rig.h.client(t, testVersion))

	assert.True(t, info.Spent.Unknown)
	assert.Equal(t, []string{"cost unknown"}, info.Notes())
}

func TestRunProgress_NotesShowTheMarksOfTheSum(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		spent agent.Spend
		limit float64
		want  []string
	}{
		{agent.Spend{Cost: 1.5, Estimated: true}, 0, []string{"spent $1.5 (estimate)"}},
		{agent.Spend{Cost: 1.5, Unknown: true}, 0, []string{"spent $1.5 (some cost unknown)"}},
		{agent.Spend{Cost: 1.5, Estimated: true, Unknown: true}, 20, []string{"spent $1.5 (estimate, some cost unknown) of $20"}},
		{agent.Spend{Unknown: true}, 20, []string{"cost unknown"}},
	} {
		assert.Equal(t, tc.want, RunProgress{Spent: tc.spent, Cap: tc.limit}.Notes(), "%+v", tc.spent)
	}
}

func TestAgentStep_ACostThatIsUnknownStopsTheNextStepWhenTheRunHasACap(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 20
	rig.reply("", unpriced())

	_, err := rig.run(t, twoAgentsFlow, "x")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cost is unknown")
	assert.Contains(t, err.Error(), "agents.max_run_budget_usd")
	assert.Len(t, rig.h.agents.seen(), 1, "the second step did not start")
}

func TestAgentStep_ACostThatIsUnknownDoesNotStopARunWithNoCap(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", unpriced())

	_, err := rig.run(t, twoAgentsFlow, "x")

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 2)
}
