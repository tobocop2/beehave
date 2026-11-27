package flow

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// commandsWith returns the commands that the rig ran for which keep is true.
func commandsWith(rig *roundRig, keep func(CommandRequest) bool) []CommandRequest {
	var found []CommandRequest

	for _, request := range rig.h.commands.requests {
		if keep(request) {
			found = append(found, request)
		}
	}

	return found
}

func TestExampleUses_TheDraftPullRequestCarriesTheBodyOfTheItem(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.opened, 1)
	assert.Contains(t, rig.h.forge.opened[0], "|a title|a body\n\nAddresses #12|integration|")
}

func TestExample_TheClosingLineFollowsTheBaseAndTheClosingKeywordsAreForbiddenOffTheTrunk(t *testing.T) {
	t.Parallel()

	for base, want := range map[string]string{"main": "Closes #12", "integration": "Addresses #12"} {
		rig, file := exampleRig(t, func(int) string { return "approve" })
		setRoundOf(&rig.project, "base", base)
		rig.baseAt(t, base)

		_, err := runPipeline(rig.h.engine(t), file)

		require.NoError(t, err, base)
		require.Len(t, rig.h.forge.opened, 1, base)
		assert.Contains(t, rig.h.forge.opened[0], "|a title|a body\n\n"+want+"|"+base+"|", base)
	}
}

func TestExampleUses_AnItemWhoseBodyClosesAnIssueIsRefusedWhenTheBaseIsNotTheTrunk(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.items = &fakeItems{fields: map[string]string{"id": testItem, "title": "a title", "body": "Closes #9"}}

	_, err := runPipeline(rig.h.engine(t), file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbid_text", "only the pull request into the trunk may close an issue")
	assert.Empty(t, rig.h.forge.opened)
}

func TestExampleUses_AnItemWhoseBodyClosesAnIssueOpensItsPullRequestWhenTheBaseIsTheTrunk(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.items = &fakeItems{fields: map[string]string{"id": testItem, "title": "a title", "body": "Closes #9"}}
	setRoundOf(&rig.project, "base", "main")
	rig.baseAt(t, "main")

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.opened, 1)
	assert.Contains(t, rig.h.forge.opened[0], "|Closes #9\n\nCloses #12|")
}

func TestExampleUses_AnApprovedItemGetsTheApprovalAndThePlanAsOneKeyedCommentOnItsPullRequest(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved)
	require.Len(t, rig.h.forge.notes, 1)
	assert.True(t, strings.HasPrefix(rig.h.forge.notes[0], testProject+"|12|"), rig.h.forge.notes[0])
	assert.Contains(t, rig.h.forge.notes[0], ":approved|The review approved the head ")
	assert.Contains(t, rig.h.forge.notes[0], "\n\nThe plan:\n\n")
}

func TestExampleUses_AnItemThatTheReviewDoesNotApproveGetsNoApprovalComment(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "reject" })
	rig.h.writer = newFakeWriter()

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Empty(t, rig.h.forge.notes)
}

func TestExampleUses_ARoundThatSetsAUnionGateLandsItsBatchesUnderThatProgram(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.setRound("union_gate", []any{"make", "union"})

	_, err := rig.run(t)

	require.NoError(t, err)

	ran := commandsWith(rig, func(request CommandRequest) bool { return slices.Equal(request.Run, []string{"make", "union"}) })
	require.NotEmpty(t, ran, "the gate of the union is the program of the round")
	assert.True(t, ran[0].Gate)
}

func TestExampleUses_ARoundThatSetsNoUnionGateRunsNoProgramForTheUnion(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, commandsWith(rig, func(request CommandRequest) bool { return request.Gate }), "the project gate judges the union")
}

func TestExampleUses_ARoundThatSetsAnAcceptanceScriptRunsItOnTheBuildHostAfterTheBatchLanded(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.setRound("acceptance", "scripts/accept.sh")

	_, err := rig.run(t)

	require.NoError(t, err)

	ran := commandsWith(rig, func(request CommandRequest) bool {
		return slices.Equal(request.Run, []string{"sh", "scripts/accept.sh"})
	})
	require.NotEmpty(t, ran)
	assert.Equal(t, "build", ran[0].Host)
	assert.Equal(t, 30*time.Minute, ran[0].Timeout)
	assert.True(t, ran[0].Gate)
}

func TestExampleUses_ARedAcceptanceScriptStopsTheRoundAndNamesTheScript(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.setRound("acceptance", "scripts/accept.sh")

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) == 2 && request.Run[1] == "scripts/accept.sh" {
			return CommandOutput{RC: 1, Stdout: "check 4 failed"}, nil
		}

		if previous == nil {
			return CommandOutput{}, nil
		}

		return previous(request)
	}

	_, err := rig.run(t)

	require.ErrorContains(t, err, "the acceptance script scripts/accept.sh failed")
	assert.Contains(t, err.Error(), "check 4 failed")
}

func TestExampleUses_ARoundWithNoAcceptanceScriptRunsNone(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, commandsWith(rig, func(request CommandRequest) bool { return request.Host == "build" && request.Gate }))
}

// withPlanText makes the plan step of the example rig answer with the text.
func withPlanText(rig *agentRig, text string) {
	answer := rig.h.agents.result

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := answer(request)
		if !strings.Contains(request.Prompt, reviewMarker) && slices.Contains(request.Outputs, "regressions") {
			result.Text = text
		}

		return result
	}
}

func TestExampleUses_ThePlanThatClosesAnIssueIsRefusedInTheApprovalCommentWhenTheBaseIsNotTheTrunk(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	withPlanText(rig, "Fix the retry.\n\nCloses #9")

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, "forbid_text", "only the pull request into the trunk may close an issue")
	assert.Empty(t, rig.h.forge.notes)
}

func TestExampleUses_ThePlanThatClosesAnIssueIsPostedInTheApprovalCommentWhenTheBaseIsTheTrunk(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	withPlanText(rig, "Fix the retry.\n\nCloses #9")
	setRoundOf(&rig.project, "base", "main")
	rig.baseAt(t, "main")

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.notes, 1)
	assert.Contains(t, rig.h.forge.notes[0], "Closes #9")
}

func TestExampleUses_ThePlanWithAnAttributionLineIsRefusedInTheApprovalComment(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	withPlanText(rig, "Fix the retry.\n\nGenerated with [Claude Code](https://claude.com/claude-code)")
	setRoundOf(&rig.project, "base", "main")
	rig.baseAt(t, "main")

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, "a line of the text matches the forbidden pattern", "the forbid_text of the comment refuses the attribution line")
	assert.Empty(t, rig.h.forge.notes, "the comment with the attribution is not posted")
}

func TestExampleUses_TheTeardownOfTheRoundNamesTheBuildHostOfTheProject(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.project.BuildHost = "box"

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"box"}, rig.hosts.clusters)
}
