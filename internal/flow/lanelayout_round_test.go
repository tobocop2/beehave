package flow

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/model"
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

// A reviewer judges one pull request at one head and cannot change it. Each review is a new session with no resume, it cannot edit, and git-write is forbidden to it,
// so no reviewer carries the judgment of one round into another round or fixes what it reviews. The fix lane resumes the implementer, never a reviewer.
func TestLaneLayout_EveryReviewIsAFreshReadOnlySessionAndNoFixLaneResumesOne(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(review int) string {
		if review < 3 {
			return "changes"
		}

		return "approve"
	})
	rig.h.writer = newFakeWriter()

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	reviews, fixes := 0, 0

	for _, request := range rig.h.agents.requests {
		switch {
		case strings.Contains(request.Prompt, reviewMarker):
			reviews++

			assert.Empty(t, request.Resume, "a review never resumes a session")
			assert.True(t, request.NoEdits, "a review cannot edit")
			assert.Contains(t, request.Forbid, model.FindingGitWrite, "a review cannot commit or push")
		case strings.Contains(request.Prompt, "This is round"):
			fixes++

			assert.NotEmpty(t, request.Resume, "a fix lane in the first rounds resumes the implementer")
		}
	}

	assert.Equal(t, 4, reviews, "three scoped reviews and the full review")
	assert.Equal(t, 2, fixes)
}

// The review loop calls the hook after each full review, so a rise in the full review of a middle candidate ends the loop at that candidate.
// A check that runs only when the loop ends misses a rise that a later fix lane hides. A scoped review judges the diff of one fix and counts no regressions of the pull request.
func TestLaneLayout_ARiseInTheFullReviewOfAMiddleCandidateHoldsTheItemBeforeTheNextFixLane(t *testing.T) {
	t.Parallel()

	// Review 1 is the scoped review of the implement and review 2 the full review of candidate 1.
	rig, file := exampleRig(t, func(review int) string {
		if review == 2 {
			return "changes"
		}

		return "approve"
	})
	withCountSequence(rig, "1", "0", "2", "0")
	writer := newFakeWriter()
	rig.h.writer = writer

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.False(t, childOf(t, result).Approved, "a rise in round one is not offered for landing, though the next review shows no rise")
	assert.Equal(t, 0, countFix(rig), "no fix lane starts after the review that showed the rise")
	assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "rose from 1 to 2")
}
