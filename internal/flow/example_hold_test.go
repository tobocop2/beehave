package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// holdingRig is the example rig in which the lane of the kind "implement" or "fix" writes the note in its output hold. Any other kind writes none. Every review gives the verdict.
// The tracker writer of the rig records the comment and the status of the item.
func holdingRig(t *testing.T, kind, note, verdict string) (*committingRig, *fakeWriter, string) {
	t.Helper()

	base, file := exampleRig(t, func(int) string { return verdict })
	rig := newCommittingRig(t, base, implementMarker, fixMarker)
	writer := newFakeWriter()
	rig.h.writer = writer

	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		done := inner(request)

		isFix := strings.Contains(request.Prompt, fixMarker)
		isImplement := strings.Contains(request.Prompt, implementMarker) && !isFix

		if (kind == "fix" && isFix) || (kind == "implement" && isImplement) {
			clean := map[string]string{"report": cleanLaneReport(kind), "uncaused": "[]", "resolved": "[]", "wontfix": "[]", "hold": note}
			answer := map[string]string{}

			for _, name := range request.Outputs {
				answer[name] = clean[name]
			}

			rig.putOutputs(request.OutputFile, answer)
		}

		return done
	}

	return rig, writer, file
}

func TestExampleHold_AnImplementLaneThatWritesAHoldEndsTheItemOnHoldBeforeTheSweep(t *testing.T) {
	t.Parallel()

	rig, writer, file := holdingRig(t, "implement", holdNote, "changes")

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.heads, 1, "only the implement step committed")

	value := childOf(t, result)
	assert.Equal(t, "needs-user", value.Reason)
	assert.False(t, value.Approved)
	assert.Equal(t, rig.heads[0], value.Head, "the item reports the head of the lane that asked")
	assert.Equal(t, []string{testProject + "|" + testItem + "|" + testRun + ":hold-note|" + holdText}, writer.notes, "one comment with the question, each option and the recommendation")
	assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses, "the item is on hold")
	assert.Len(t, rig.h.agents.requests, 2, "the plan and the implement step ran, and no agent after them")
	assert.Equal(t, -1, agentIndex(rig.agentRig, sweepMarker), "no sweep ran")
	assert.Empty(t, rig.h.forge.pushed, "nothing was pushed")
	assert.Empty(t, rig.h.forge.opened, "no draft was opened")
}

func TestExampleHold_AnImplementLaneThatWritesNoHoldRunsOnToTheSweepAndTheDraft(t *testing.T) {
	t.Parallel()

	rig, writer, file := holdingRig(t, "none", "", "approve")

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Empty(t, writer.notes, "an empty hold writes no comment")
	assert.Empty(t, writer.statuses, "an empty hold sets no status")
	assert.NotEqual(t, -1, agentIndex(rig.agentRig, sweepMarker), "the sweep ran")
	assert.Len(t, rig.h.forge.opened, 1, "the draft opened")
}

func TestExampleHold_AFixLaneThatWritesAHoldEndsTheLoopWithNoMoreAgent(t *testing.T) {
	t.Parallel()

	rig, writer, file := holdingRig(t, "fix", holdNote, "changes")

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.heads, 2, "the implement step and the first fix committed")

	value := childOf(t, result)
	assert.Equal(t, "needs-user", value.Reason)
	assert.False(t, value.Approved)
	assert.Equal(t, rig.heads[1], value.Head, "the item reports the head of the fix that asked")
	assert.Equal(t, []string{testProject + "|" + testItem + "|" + testRun + ":hold-note|" + holdText}, writer.notes, "one comment with the question, each option and the recommendation")
	assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses, "the item is on hold")
	assert.Equal(t, len(rig.h.agents.requests)-1, agentIndex(rig.agentRig, fixMarker), "the fix is the last agent of the run: no review and no second fix after it")
	assert.Len(t, rig.h.forge.pushed, 1, "the push of the implement step is the only push: the fix that asked is not pushed")
}
