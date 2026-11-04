package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

func alwaysChanges(int) string { return "changes" }

func sameMechanism(int) string { return "internal/x/loop.go:retry" }

func noFindings(int) string { return "[]" }

func TestExample_TheThirdRoundOnOneMechanismRunsTheDesignStepAndAReplaceEndsTheLoop(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, alwaysChanges, noFindings, sameMechanism, `{"decision":"replace","reason":"the framework already retries"}`)
	writer := newFakeWriter()
	rig.h.writer = writer

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus", "opus"}, rig.models(), "plan, implement, three reviews, two fixes, then the design step and no third fix")
	assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "replace")
	assert.Contains(t, writer.notes[0], "the framework already retries")
	assert.Empty(t, result.Merge)
}

func TestExample_TheLanesOfTheItemAreFixReviewReworkAndDesignInTheLedgerOfTheMethod(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, alwaysChanges, noFindings, sameMechanism, `{"decision":"replace","reason":"the framework already retries"}`)
	rig.h.writer = newFakeWriter()

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	lanes, err := rig.h.client(t, testVersion).Lanes(testItem)
	require.NoError(t, err)

	var kinds []string
	for _, lane := range lanes.Lanes {
		kinds = append(kinds, lane.Kind)
	}

	assert.Equal(t, []string{"fix", "fix", "review", "rework", "review", "rework", "review", "design"}, kinds,
		"the plan and the implement step are fix lanes, each fix after a review is a rework lane, and the design step is a design lane")
}

func TestExample_ADeleteDecisionAlsoEndsTheLoop(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, alwaysChanges, noFindings, sameMechanism, `{"decision":"delete","reason":"nothing needs it"}`)
	rig.h.writer = newFakeWriter()

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Len(t, rig.models(), 8, "the loop ends at the design step")
}

func TestExample_AKeepDecisionRunsTheFixWithTheReasonAndDoesNotAskAgainForThatMechanism(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, func(review int) string {
		if review <= 4 {
			return "changes"
		}

		return "approve"
	}, noFindings, sameMechanism, `{"decision":"keep","reason":"the retry is the smallest part"}`)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus", "opus", "sonnet", "opus", "opus", "opus"}, rig.models(),
		"the design step runs once: the third fix follows it, and the fourth review gets a plain fix (on opus, from round 4)")

	requests := rig.h.agents.requests
	assert.Contains(t, requests[8].Prompt, "the retry is the smallest part", "the fix after the design step gets the reason")
	assert.NotContains(t, requests[10].Prompt, "the retry is the smallest part", "a later fix on the same mechanism is a plain fix")
}

func TestExample_FindingsOnDifferentMechanismsKeepTheNormalFixRounds(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, func(review int) string {
		if review <= 3 {
			return "changes"
		}

		return "approve"
	}, noFindings, func(review int) string { return []string{"", "a.go", "b.go", "c.go", "d.go"}[review] }, `{"decision":"replace","reason":"unused"}`)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus"}, rig.models(), "no design step: no mechanism had two fixes")
}

func TestExample_AnEmptyMechanismNeverCountsAsOneMechanism(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, func(review int) string {
		if review <= 3 {
			return "changes"
		}

		return "approve"
	}, noFindings, func(int) string { return " " }, `{"decision":"replace","reason":"unused"}`)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus"}, rig.models())
}

func TestExample_WithoutADesignPromptAReviewThatWritesNoMechanismPassesTheLoop(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	}, noFindings, nil, "")

	root, err := os.OpenRoot(filepath.Dir(file))
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, root.Close()) })

	source, err := root.ReadFile("workflow.star")
	require.NoError(t, err)

	plain := strings.Replace(string(source), `, design_prompt = "prompts/design.md"`, "", 1)
	require.NotEqual(t, string(source), plain, "the example opts into the design round")
	require.NoError(t, root.WriteFile("workflow.star", []byte(plain), 0o600))

	_, err = rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus"}, rig.models(), "plan, implement, review, fix, review")
}

func TestExample_TheCounterKeysOnTheFileSoAFunctionSuffixDoesNotSplitIt(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, alwaysChanges, noFindings, func(review int) string { return []string{"", "a.go", "a.go:f", "a.go : g"}[review] }, `{"decision":"replace","reason":"unused"}`)
	rig.h.writer = newFakeWriter()

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus", "opus"}, rig.models(), "the third review reaches the design step")
}

func TestExample_ADesignStepThatFailsFailsTheRunAndIsNotReadAsKeep(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, alwaysChanges, noFindings, sameMechanism, `{"decision":"keep","reason":"x"}`)
	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)
		if strings.Contains(request.Prompt, designMarker) {
			result.Outcome = agent.OutcomeTimedOut
		}

		return result
	}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus", "opus"}, rig.models(), "the design step is the last agent call: no fix follows it")
}

func TestExample_ADesignStepThatChangedTheFolderStopsTheRun(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, alwaysChanges, noFindings, sameMechanism, `{"decision":"keep","reason":"x"}`)
	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)
		if strings.Contains(request.Prompt, designMarker) {
			rig.put(filepath.Join(rig.dir, "edited.txt"), "a shell write of the design step")
			gitIn(t, rig.dir, "add", "edited.txt")
		}

		return result
	}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "the design step changed the item folder")
}

func TestExample_ADecisionThatIsNotListedStopsTheRun(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, alwaysChanges, noFindings, sameMechanism, `{"decision":"rewrite","reason":"x"}`)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "rewrite")
}
