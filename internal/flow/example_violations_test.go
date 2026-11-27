package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

func TestExample_AViolationOfTheImplementStepSendsTheLoopToAFixBeforeAnyReview(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)
		if !isSweep(request) && request.Resume == "" && !request.NoEdits && !strings.Contains(request.Prompt, reviewMarker) {
			result.Violations = []string{"bulk-staging: git add -A"}
		}

		return result
	}

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)

	var kinds []string
	for _, request := range rig.h.agents.seen() {
		kinds = append(kinds, kindOf(request))
	}

	require.GreaterOrEqual(t, len(kinds), 5)
	assert.Equal(t, []string{"plan", "implement", "sweep", "fix", "review"}, kinds[:5], "the sweep carries the violation of the implement step to the first fix, which comes before the first review")
}

func TestExample_AnImplementStepOnARunnerWithNoSkillLineIsRefusedBeforeAnyAgentStarts(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.RunnerSkillLoads = map[string]bool{"oc": false}
	rig.setRound("steps", map[string]any{"implement": map[string]any{"runner": "oc"}})

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, "runner oc of step implement has no line that loads a skill before the first turn")
	assert.Empty(t, rig.h.agents.seen(), "no agent started: the plan step spent no call")
}

func TestExample_AViolationOfTheSweepStepSendsTheLoopToAFixBeforeAnyReview(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)
		if isSweep(request) {
			result.Violations = []string{"bulk-staging: git add -A"}
		}

		return result
	}

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)

	var kinds []string
	for _, request := range rig.h.agents.seen() {
		kinds = append(kinds, kindOf(request))
	}

	require.GreaterOrEqual(t, len(kinds), 5)
	assert.Equal(t, []string{"plan", "implement", "sweep", "fix", "review"}, kinds[:5], "the violation of the sweep reaches the first fix, which comes before the first review")
}
