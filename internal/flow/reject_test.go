package flow

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func TestAgentStep_ForbiddenFollowUpsKeepARejectWhateverTheFindings(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"reject","findings":[{"class":"blocking","text":"the design is wrong"}]}`, exited)

	_, err := rig.run(t, followUpFlow(`follow_ups = "forbid", `), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"show reject"}, rig.h.commands.ran())
}

func TestExample_TheReviewIsReadOnlyAndTheFixIsNot(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	}, func(int) string { return "[]" })

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)

	var reviews, others, guarded int

	for _, request := range rig.h.agents.requests {
		if slices.Contains(request.Forbid, model.FindingProtected) {
			guarded++

			assert.Equal(t, []string{"/own/checkout"}, request.Protect, "the checkout of the owner is off limits")
		}

		if strings.Contains(request.Prompt, reviewMarker) {
			reviews++

			assert.Contains(t, request.Forbid, model.FindingGitWrite, "the review must not change a repository")
			assert.True(t, request.NoEdits, "the review starts without the permission to edit files")

			continue
		}

		others++

		assert.NotContains(t, request.Forbid, model.FindingGitWrite, "the fixer commits and pushes")
	}

	assert.Equal(t, 2, reviews)
	assert.Equal(t, 3, others, "plan, implement and one fix")
	assert.Equal(t, 4, guarded, "implement, two reviews and one fix hold the protected checkout; the plan does not")
}

func TestExample_AFailingGateRunsNoReviewSoARejectNeverFollowsAFixRound(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(int) string { return "reject" }, func(int) string { return "[]" })
	rig.h.gate.results = []GateResult{{Passed: false, Head: rig.head, Report: "lint: unused variable x"}, {Passed: true, Head: rig.head, Tree: exampleTree}}
	writer := newFakeWriter()
	rig.h.writer = writer

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "sonnet", "opus"}, rig.models(), "plan, implement, one fix for the failed gate, then one review that rejects with no second fix")
	assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses)
}

func TestExample_ARejectVerdictEndsTheLoopWithNoFixAndHoldsTheItemWithTheReason(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(int) string { return "reject" }, func(int) string {
		return `[{"class":"blocking","text":"the design is wrong"}]`
	})
	writer := newFakeWriter()
	rig.h.writer = writer

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models(), "plan, implement and one review; no fix round")
	assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "all done")
	assert.Empty(t, result.Merge)
}
