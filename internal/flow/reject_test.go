package flow

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestExample_TheReviewIsReadOnlyAndTheFixIsNot(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	}, func(int) string { return "[]" })

	_, err := runPipeline(rig.h.engine(t), file)

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

	assert.Equal(t, 3, reviews, "two scoped reviews and the full review")
	assert.Equal(t, 4, others, "plan, implement, the sweep and one fix")
	assert.Equal(t, 6, guarded, "implement, the sweep, three reviews and one fix hold the protected checkout; the plan does not")
}

func TestExample_AFailingGateRunsNoFullReviewAndARejectAfterItsFixEndsTheLoop(t *testing.T) {
	t.Parallel()

	// The scoped review of the implement approves, so the candidate reaches the gate. Every later review rejects.
	rig, file := exampleRigWithFindings(t, func(review int) string {
		if review == 1 {
			return "approve"
		}

		return "reject"
	}, func(int) string { return "[]" })
	rig.h.gate.results = []GateResult{{Passed: false, Head: rig.head, Report: "lint: unused variable x"}, {Passed: true, Head: rig.head, Tree: exampleTree}}
	writer := newFakeWriter()
	rig.h.writer = writer

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus"}, rig.models(), "plan, implement, the scoped review, one fix for the failed gate, then a scoped review that rejects with no second fix")
	assert.Equal(t, 1, rig.h.gate.calls, "the red gate gets no full review, and the reject ends the loop before a second gate")
	assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses)
}

func TestExample_ARejectVerdictEndsTheLoopWithNoFixAndHoldsTheItemWithTheReason(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(int) string { return "reject" }, func(int) string {
		return `[{"id":"","severity":"blocking","path":"a.go","text":"the design is wrong"}]`
	})
	writer := newFakeWriter()
	rig.h.writer = writer

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models(), "plan, implement and the scoped review; no fix round")
	assert.Zero(t, rig.h.gate.calls, "a reject of the scoped review ends the loop before the first gate")
	assert.Equal(t, rig.head, childOf(t, result).Head, "the held item reports the head that the review judged")
	assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "all done")
	assert.False(t, childOf(t, result).Approved)
}
