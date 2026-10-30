package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

// The engine takes the claim of the item before the first step of workflow.star and gives it back after the last one.
// These tests run the example file itself, so a change that moves a step ahead of the claim fails here.

func TestExampleClaim_TheItemIsClaimedBeforeTheFirstAgentStepAndReleasedAfterTheLast(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	var atFirstAgent []string

	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		if atFirstAgent == nil {
			atFirstAgent = rig.h.life.seen()
		}

		return inner(request)
	}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"claim:" + testItem}, atFirstAgent, "the claim is the only lifecycle step before the first agent starts")
	assert.Equal(t, []string{"claim:" + testItem, "ready:" + testItem, "release:" + testItem}, rig.h.life.seen(), "the claim is first and the release is last")
	assert.Equal(t, []string{testOwner + testItem}, rig.h.life.releasedBy(), "the release carries the token of the claim")
}

func TestExampleClaim_AFailedClaimStartsNoAgent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.life.failing["claim"] = true

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, rig.h.agents.seen(), "a run that holds no claim starts no agent, so a store that cannot be read fails closed")
	assert.Empty(t, rig.h.life.releasedBy(), "a claim that was not taken is not released")
}

func TestExampleClaim_TheItemIsReleasedWhenAStepOfTheExampleFails(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.agents.err = errFake

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Equal(t, []string{testOwner + testItem}, rig.h.life.releasedBy(), "the release runs on a failed run, with the token of the claim")
}
