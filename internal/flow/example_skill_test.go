package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// ownSkill is a skill that a project names in vars.review_skill in place of exampleSkill.
const ownSkill = "the-own-review-skill"

// sweepMarker is a phrase that only the prompt of the sweep step holds.
const sweepMarker = "Iterate until a pass finds no defect"

// isSweep says whether the request is the call of the sweep step of the example.
func isSweep(request AgentRequest) bool { return strings.Contains(request.Prompt, sweepMarker) }

// sweepStepRequest returns the request of the sweep step of the example: the third agent call of the run, after the plan and the implement step.
func sweepStepRequest(t *testing.T, rig *agentRig) AgentRequest {
	t.Helper()

	require.GreaterOrEqual(t, len(rig.h.agents.requests), 3, "the plan step, the implement step and the sweep step started")

	request := rig.h.agents.requests[2]
	require.Contains(t, request.Prompt, sweepMarker, "the third agent call is the sweep")

	return request
}

func TestExample_TheSweepStartsRightAfterTheImplementStepWithTheReviewSkillLoadedFirst(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	implement := rig.h.agents.requests[1]
	sweep := sweepStepRequest(t, rig)

	assert.True(t, strings.HasPrefix(sweep.Prompt, "/fake-skills:"+exampleSkill+"\n"), "the skill line of the adapter is the first line of the sweep prompt: %q", sweep.Prompt)
	assert.Equal(t, []string{exampleSkill}, sweep.Skills, "the sweep gets the review skill that the project names")
	assert.Equal(t, "sess-1", sweep.Resume, "the sweep continues the session of the implement step")
	assert.Equal(t, implement.Model, sweep.Model, "the sweep runs on the model of the implement step")
	assert.Equal(t, implement.Login, sweep.Login, "the sweep runs on the login of the implement step")
	assert.Equal(t, implement.Sandbox, sweep.Sandbox, "the sweep has the sandbox of the implement step")
	assert.False(t, sweep.NoEdits, "the sweep may edit")
	assert.Equal(t, implement.Forbid, sweep.Forbid)
	assert.Equal(t, implement.Protect, sweep.Protect)
}

func TestExample_AProjectThatNamesAnotherSkillGetsItInTheSweepAndNoRefusalOnTheSkillCallsOfTheAgent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["review_skill"] = ownSkill

	reporting := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := reporting(request)
		result.Skills = nil

		return result
	}

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err, "no check reads the skills that an agent called, so an agent that called none is not refused")
	assert.Positive(t, rig.h.gate.calls, "the item reached the gate")
	sweep := sweepStepRequest(t, rig)

	assert.True(t, strings.HasPrefix(sweep.Prompt, "/fake-skills:"+ownSkill+"\n"))
	assert.Equal(t, []string{ownSkill}, sweep.Skills)
}

func TestExample_TheStepsGetTheSkillsThatTheirPromptsDeclareAndTheReviewSkillComesFromTheProject(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["review_skill"] = ownSkill

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	assert.Equal(t, []string{ownSkill}, rig.h.agents.requests[0].Skills, "the plan step orders the review skill, so it gets it")
	require.Len(t, rig.h.agents.requests[1].Skills, 3, "the implement step gets the three skills that its prompt orders")
	assert.Equal(t, ownSkill, rig.h.agents.requests[1].Skills[2], "the review skill of the project comes last")
	assert.Contains(t, rig.h.agents.requests[1].Prompt, "`"+ownSkill+"` iterated to zero", "the prompt renders the plain name of the review skill")
}

func TestExample_AProjectWithNoUsableReviewSkillIsRefusedBeforeAnyAgentStarts(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]any{"a missing key": nil, "a blank text": "  ", "a number": 3, "a list": []any{exampleSkill}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, file := exampleRig(t, func(int) string { return "approve" })
			if value == nil {
				delete(rig.project.Vars, "review_skill")
			} else {
				rig.project.Vars["review_skill"] = value
			}

			_, err := runPipeline(rig.h.engine(t), file)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "vars.review_skill")
			assert.Contains(t, err.Error(), "project file")
			assert.Empty(t, rig.h.agents.requests, "the refusal comes before the plan agent")
		})
	}
}

func TestExample_AnImplementOnARunnerWithNoSkillLineIsRefusedAtTheSweepAndNoGateRuns(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.skillLine = func(string, string) string { return "" }

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, "runner "+testLogin, "the refusal names the runner")
	require.ErrorContains(t, err, exampleSkill)
	assert.Len(t, rig.h.agents.requests, 2, "the plan step and the implement step ran, and the sweep did not start")
	assert.Zero(t, rig.h.gate.calls, "no gate runs when the sweep did not start")
}

func TestExample_AFullReviewThatApprovesWithAnOpenFindingIsAChangesVerdictAndSendsTheRunToAFix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(int) string { return "approve" }, func(review int) string {
		if review == 2 {
			return `[{"class":"minor","text":"rename x"}]`
		}

		return "[]"
	})

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "opus", "sonnet", "opus", "opus"}, rig.models(), "the scoped review approved, the full review approved with a finding open and sent the run to a fix, then the next scoped review and full review approved")
	assert.True(t, childOf(t, result).Approved)
}
