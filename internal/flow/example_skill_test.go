package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

func TestExample_AnImplementStepWhoseAgentReportsNoSkillStopsTheItemBeforeTheGate(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	reporting := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := reporting(request)
		result.Skills = nil

		return result
	}

	_, err := runPipeline(rig.h.engine(t), file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "did not call the skill "+exampleSkill)
	assert.Contains(t, err.Error(), "run beehave resume "+testItem+", then beehave cancel "+testItem+", then beehave run "+testItem, "the text names the steps that start the item again")
	assert.NotContains(t, err.Error(), "skill, then run beehave resume", "a plain resume replays the stop and is not the instruction")
	assert.Zero(t, rig.h.gate.calls, "no gate runs when the implement step reported no skill")
	assert.Equal(t, []string{"opus", "sonnet"}, rig.models(), "the plan step and the implement step ran, and no review ran after them")
}

// ownSkill is a skill that a project names in vars.review_skill in place of exampleSkill.
const ownSkill = "the-own-review-skill"

func TestExample_AProjectThatNamesAnotherSkillIsRefusedWhenItsAgentCallsOnlyTheOldOne(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["review_skill"] = ownSkill

	_, err := runPipeline(rig.h.engine(t), file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not call the skill "+ownSkill)
	assert.Contains(t, err.Error(), "vars.review_skill", "the refusal names the setting")
	assert.NotContains(t, err.Error(), exampleSkill)
	assert.Zero(t, rig.h.gate.calls, "no gate runs when the agent called only the skill of another project")
}

func TestExample_AProjectThatNamesAnotherSkillPassesWhenItsAgentCallsIt(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["review_skill"] = ownSkill

	reporting := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := reporting(request)
		result.Skills = []string{ownSkill}

		return result
	}

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Positive(t, rig.h.gate.calls, "the item reached the gate")
}

func TestExample_TheStepsGetTheSkillsThatTheirPromptsDeclareAndTheReviewSkillComesFromTheProject(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["review_skill"] = ownSkill

	_, err := runPipeline(rig.h.engine(t), file)
	require.Error(t, err, "the agent called only the old skill")

	require.Len(t, rig.h.agents.requests, 2, "the plan step and the implement step")
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

// runnerForImplement puts the implement step of the example on the named runner, and says whether that runner reports skill calls.
func runnerForImplement(t *testing.T, rig *agentRig, runner string, reportsSkills bool) {
	t.Helper()

	settings, ok := rig.project.Vars["round"].(map[string]any)
	require.True(t, ok)

	steps, ok := settings["steps"].(map[string]any)
	require.True(t, ok)

	steps["implement"] = map[string]any{"model": "sonnet", "runner": runner}
	rig.project.RunnerSkills = map[string]bool{runner: reportsSkills}
}

func TestExample_AnImplementStepOnARunnerThatReportsNoSkillCallsIsRefusedBeforeAnyAgentStarts(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	runnerForImplement(t, rig, "oc", false)

	_, err := runPipeline(rig.h.engine(t), file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "runner oc", "the refusal names the runner")
	assert.Contains(t, err.Error(), "step implement", "the refusal names the step")
	assert.Empty(t, rig.h.agents.requests, "no agent started, so nothing was spent")
}

func TestExample_AnImplementStepOnARunnerThatReportsSkillCallsIsNotRefusedAtTheStart(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	runnerForImplement(t, rig, "cl", true)

	_, err := runPipeline(rig.h.engine(t), file)
	if err != nil {
		assert.NotContains(t, err.Error(), "report skill calls")
	}

	assert.NotEmpty(t, rig.h.agents.requests, "the plan step started")
}
