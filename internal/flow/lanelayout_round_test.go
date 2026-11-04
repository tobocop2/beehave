package flow

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

// withCountSequence makes the plan write the count plan and the nth review write reviews[n-1]. A review past the list repeats its last count.
func withCountSequence(rig *agentRig, plan string, reviews ...string) {
	inner := rig.h.agents.result
	seen := 0

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)

		switch {
		case strings.Contains(request.Prompt, reviewMarker):
			seen++
			overrideCount(rig, request.OutputFile, reviews[min(seen, len(reviews))-1])
		case slices.Contains(request.Outputs, "regressions"):
			rig.put(request.OutputFile, `{"regressions":`+strconv.Quote(plan)+`}`)
		}

		return result
	}
}

// The review loop calls the hook after each review, so a rise in a middle round ends the loop at that round.
// A check that runs only when the loop ends misses a rise that a later fix lane hides.
func TestLaneLayout_ARiseInAMiddleRoundHoldsTheItemBeforeTheNextFixLane(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	})
	withCountSequence(rig, "1", "2", "0")
	rig.h.opts.Env["MERGE"] = "true"
	writer := newFakeWriter()
	rig.h.writer = writer

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Empty(t, result.Merge, "a rise in round one blocks the merge, though the next review shows no rise")
	assert.Equal(t, 0, countFix(rig), "no fix lane starts after the review that showed the rise")
	assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "rose from 1 to 2")
}
