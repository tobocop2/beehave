package main

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

var w3cTraceparent = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)

// wantTraceparent restates the rule with the standard library: trace id = sha256("beehave-trace:" + run)[:16], span id = sha256("beehave-span:" + run + "/" + call)[:8].
func wantTraceparent(run, call string) string {
	trace := sha256.Sum256([]byte("beehave-trace:" + run))
	span := sha256.Sum256([]byte("beehave-span:" + run + "/" + call))

	return "00-" + hex.EncodeToString(trace[:16]) + "-" + hex.EncodeToString(span[:8]) + "-01"
}

func specOfStep(t *testing.T, key bool, run, call string) agent.Spec {
	t.Helper()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.traceParent = key

	req := request()
	req.Run, req.Call = run, call

	_, err := f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	return *f.spec
}

func TestAgents_TheKeyOnPutsTheTraceparentOfTheRunAndTheStepInTheSpec(t *testing.T) {
	t.Parallel()

	got := specOfStep(t, true, "run-1", "agent:fix#1").Traceparent

	assert.Equal(t, wantTraceparent("run-1", "agent:fix#1"), got)
	assert.Regexp(t, w3cTraceparent, got)
}

func TestAgents_TheKeyOffLeavesTheSpecWithNoTraceparent(t *testing.T) {
	t.Parallel()

	assert.Empty(t, specOfStep(t, false, "run-1", "agent:fix#1").Traceparent)
}

func TestAgents_TwoStepsOfOneRunShareTheTraceIdAndDifferInTheSpanId(t *testing.T) {
	t.Parallel()

	a := specOfStep(t, true, "run-1", "agent:fix#1").Traceparent
	b := specOfStep(t, true, "run-1", "agent:review#1").Traceparent

	assert.Equal(t, a[3:35], b[3:35])
	assert.NotEqual(t, a[36:52], b[36:52])
}

func TestAgents_TwoRunsHaveTwoTraceIds(t *testing.T) {
	t.Parallel()

	a := specOfStep(t, true, "run-1", "agent:fix#1").Traceparent
	b := specOfStep(t, true, "run-2", "agent:fix#1").Traceparent

	assert.NotEqual(t, a[3:35], b[3:35])
}

func TestAgents_AReplayOfOneStepBuildsTheSameTraceparent(t *testing.T) {
	t.Parallel()

	a := specOfStep(t, true, "run-1", "agent:fix#1").Traceparent
	b := specOfStep(t, true, "run-1", "agent:fix#1").Traceparent

	assert.NotEmpty(t, a)
	assert.Equal(t, a, b)
}

func TestAgents_TheKeyDoesNotAddATraceVariableToTheEngineEnvironment(t *testing.T) {
	t.Parallel()

	assert.Equal(t, map[string]string{"PATH": "/bin"}, specOfStep(t, true, "run-1", "agent:fix#1").Env)
}
