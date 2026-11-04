package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
)

// usageAdapter reads a line "tokens=<in>,<out>" as the usage of a turn, and "cost=<dollars>" as the cost of the run.
type usageAdapter struct{ agent.Adapter }

func (usageAdapter) Line(string) (string, string) { return "", "" }

func (usageAdapter) Calls(line string) agent.Seen {
	if rest, ok := strings.CutPrefix(line, "tokens="); ok {
		in, out, _ := strings.Cut(rest, ",")

		return agent.Seen{Usage: agent.Tokens{Input: tokenCount(in), Output: tokenCount(out)}}
	}

	if rest, ok := strings.CutPrefix(line, "cost="); ok {
		return agent.Seen{Cost: float64(tokenCount(rest))}
	}

	return agent.Seen{}
}

func tokenCount(text string) int64 {
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0
	}

	return n
}

func tapWithPrices(t *testing.T) *sessionTap {
	t.Helper()

	prices, err := agent.ParsePrices([]byte(`{"models":{"m":{"input":1,"output":10}}}`))
	require.NoError(t, err)

	return newSessionTap(usageAdapter{}, prices)
}

func TestSessionTap_EstimatesTheCostFromTheSumOfTheTokensWhenTheCLIReportsNoCost(t *testing.T) {
	t.Parallel()

	tap := tapWithPrices(t)

	_, err := tap.Write([]byte("tokens=1000000,0\ntokens=0,100000\n"))
	require.NoError(t, err)

	spend := tap.spent("m")

	assert.InDelta(t, 2.0, spend.Cost, 1e-9)
	assert.Equal(t, agent.Spend{Cost: spend.Cost, Estimated: true}, spend)
}

func TestSessionTap_ACostThatTheCLIReportsIsNeverAnEstimate(t *testing.T) {
	t.Parallel()

	tap := tapWithPrices(t)

	_, err := tap.Write([]byte("tokens=1000000,0\ncost=3\n"))
	require.NoError(t, err)

	assert.Equal(t, agent.Spend{Cost: 3}, tap.spent("m"))
}

func TestSessionTap_AModelWithNoPriceHasAnUnknownCostNotAZeroCost(t *testing.T) {
	t.Parallel()

	tap := tapWithPrices(t)

	_, err := tap.Write([]byte("tokens=1000000,0\n"))
	require.NoError(t, err)

	for _, name := range []string{"unknown", ""} {
		assert.Equal(t, agent.Spend{Unknown: true}, tap.spent(name), name)
	}
}

func TestSessionTap_ARunWithNoTokensAndNoReportedCostCostsNothing(t *testing.T) {
	t.Parallel()

	assert.Equal(t, agent.Spend{}, tapWithPrices(t).spent("m"))
}

// flaglessAdapter is an adapter whose CLI has no option to cap the dollars of a run.
type flaglessAdapter struct{ lineAdapter }

func (flaglessAdapter) Profile() agent.Profile { return agent.Profile{} }

func TestAgents_ACostCapOnACLIThatDoesNotCapDollarsIsRefusedWhenTheModelHasNoPrice(t *testing.T) {
	t.Parallel()

	for name, set := range map[string]func(*agentFixture, *flow.AgentRequest){
		"the_cap_of_the_run":    func(f *agentFixture, _ *flow.AgentRequest) { f.agents.maxRunBudget = 20 },
		"the_global_step_cap":   func(f *agentFixture, _ *flow.AgentRequest) { f.agents.maxBudget = 5 },
		"the_cap_of_the_step":   func(_ *agentFixture, r *flow.AgentRequest) { r.MaxBudget = 2 },
		"the_cap_and_the_model": func(f *agentFixture, r *flow.AgentRequest) { f.agents.maxRunBudget, r.Model = 20, "" },
	} {
		f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
		f.agents.adapter = func(model.Agent) agent.Adapter { return flaglessAdapter{} }
		req := request()
		set(f, &req)

		got, err := f.agents.Run(t.Context(), req)

		require.NoError(t, err, name)
		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, name)
		assert.Contains(t, got.Note, "has no price", name)
		assert.Contains(t, got.Note, "agents.max_run_budget_usd", name)
		assert.Empty(t, f.spec.Dir, "%s: the run did not start", name)
	}
}

func TestAgents_ACostCapOnACLIThatDoesNotCapDollarsStartsWhenTheModelHasAPrice(t *testing.T) {
	t.Parallel()

	prices, err := agent.ParsePrices([]byte(`{"models":{"opus":{"input":1,"output":10}}}`))
	require.NoError(t, err)

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.adapter = func(model.Agent) agent.Adapter { return flaglessAdapter{} }
	f.agents.maxRunBudget, f.agents.prices = 20, prices

	_, err = f.agents.Run(t.Context(), request())

	require.NoError(t, err)
	assert.Equal(t, "/work/tree", f.spec.Dir, "the run started")
}

func TestAgents_ARunWithNoCostCapStartsWhateverTheModel(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.adapter = func(model.Agent) agent.Adapter { return flaglessAdapter{} }

	_, err := f.agents.Run(t.Context(), request())

	require.NoError(t, err)
	assert.Equal(t, "/work/tree", f.spec.Dir)
}
