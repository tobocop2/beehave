package flow

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// withCounts makes the plan agent and the review agents of the example rig write the given texts as their output `regressions`.
// The plan agent is the request that declares the output without the review marker. A review is a request that holds the review marker.
func withCounts(rig *agentRig, plan, review string) {
	inner := rig.h.agents.result

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)

		switch {
		case strings.Contains(request.Prompt, reviewMarker):
			overrideCount(rig, request.OutputFile, review)
		case slices.Contains(request.Outputs, "regressions"):
			rig.put(request.OutputFile, `{"regressions":`+strconv.Quote(plan)+`}`)
		}

		return result
	}
}

// overrideCount sets the output `regressions` in the output file that the rig wrote for a review, and keeps the other fields. A review that wrote no file stays without one.
func overrideCount(rig *agentRig, file, count string) {
	raw, err := os.ReadFile(file) //nolint:gosec // the test names its own output file
	if err != nil {
		return
	}

	var written map[string]any
	if err := json.Unmarshal(raw, &written); err != nil {
		return
	}

	written["regressions"] = count

	body, err := json.Marshal(written)
	if err != nil {
		return
	}

	rig.put(file, string(body))
}

func countFix(rig *agentRig) int {
	fixes := 0

	for _, request := range rig.h.agents.requests {
		if strings.Contains(request.Prompt, "This is round") {
			fixes++
		}
	}

	return fixes
}

// countCandidates is the number of candidates that reached the full review: each candidate gets one review of the whole pull request.
func countCandidates(rig *agentRig) int {
	candidates := 0

	for _, request := range rig.h.agents.requests {
		if strings.Contains(request.Prompt, fullReview) {
			candidates++
		}
	}

	return candidates
}

func TestLaneLayout_APatchRoundsCapOfTwoGivesThreeCandidatesAndThenAsksTheOwner(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	rig.setRound("fix_rounds", json.Number("2"))
	done := pipelineInBackground(rig.h.engine(t), file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, 3, countCandidates(rig), "two fix rounds need three candidates: the third checks the second fix round, then the owner rules")
	assert.Equal(t, 3, rig.h.gate.calls, "one gate for each candidate")
	assert.Equal(t, slices.Concat([]string{"opus", "sonnet"}, capModels(3), []string{"sonnet"}), rig.models(), "plan, implement, three candidates, and the settle step")
}

func TestLaneLayout_TheDefaultCapIsSixCandidates(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	done := pipelineInBackground(rig.h.engine(t), file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, 6, countCandidates(rig), "five fix rounds and the candidate that checks the last one")
}

func TestLaneLayout_ARiseInTheRegressionCountHoldsTheItemAndMergesNothing(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	withCounts(rig, "1", "2")
	writer := newFakeWriter()
	rig.h.writer = writer

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.False(t, childOf(t, result).Approved, "a rise is not offered to the round for landing")
	assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "regression count rose from 1 to 2")
}

func TestLaneLayout_ASameOrLowerRegressionCountDoesNotBlock(t *testing.T) {
	t.Parallel()

	for _, end := range []string{"1", "0"} {
		rig, file := exampleRig(t, func(int) string { return "approve" })
		withCounts(rig, "1", end)

		result, err := runPipeline(rig.h.engine(t), file)

		require.NoError(t, err)
		assert.True(t, childOf(t, result).Approved, "end count %s", end)
	}
}

// runHeld runs the example with a plan count and a review count that approve, and returns the result, the comments and the statuses.
func runHeld(t *testing.T, plan, review string) (ItemResult, *fakeWriter) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	withCounts(rig, plan, review)
	writer := newFakeWriter()
	rig.h.writer = writer

	result, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	return result, writer
}

func TestLaneLayout_ACountWithSpacesAroundTheNumberIsReadAndARiseHolds(t *testing.T) {
	t.Parallel()

	for _, shape := range []string{"2", " 2", "2 ", "\t2\n"} {
		_, writer := runHeld(t, "1", shape)

		require.Len(t, writer.notes, 1, shape)
		assert.Contains(t, writer.notes[0], "rose from 1 to 2", shape)
	}
}

func TestLaneLayout_AnUnknownRegressionCountHoldsTheItemAndMergesNothing(t *testing.T) {
	t.Parallel()

	for name, reports := range map[string][2]string{
		"an empty text on the review": {"0", ""},
		"an empty text on the plan":   {"", "0"},
		"a word on the review":        {"0", "many"},
		"a fraction on the plan":      {"1.5", "0"},
		"the placeholder":             {"0", "<n>"},
		"a number with a note":        {"0", "2 (one is new)"},
		"a negative number":           {"0", "-1"},
		"the old report line":         {"0", "Regressions of this pull request so far: 0"},
	} {
		result, writer := runHeld(t, reports[0], reports[1])

		assert.False(t, childOf(t, result).Approved, name)
		assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses, name)
		require.Len(t, writer.notes, 1, name)
		assert.Contains(t, writer.notes[0], "no regression count that can be read", name)
	}
}

func TestLaneLayout_AnEqualCountMerges(t *testing.T) {
	t.Parallel()

	result, writer := runHeld(t, "1", "1")

	assert.True(t, childOf(t, result).Approved)
	assert.Empty(t, writer.statuses)
}

// The baseline lives in the prompts: the review counts the regressions open at its head (pinned by TestPrompts), and the workflow compares that count with the plan.
// This test shows the compare only: a fix round between two equal counts holds nothing.
func TestLaneLayout_AFixRoundBetweenTwoEqualCountsDoesNotHoldTheApprovedItem(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	})
	withCounts(rig, "0", "0")
	rig.h.writer = newFakeWriter()

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, 1, countFix(rig))
	assert.True(t, childOf(t, result).Approved)
}

func TestLaneLayout_ARiseAtTheCapHoldsTheItemBeforeTheOwnerIsAskedAndNoSettleLaneStarts(t *testing.T) {
	t.Parallel()

	for _, review := range []string{"1", "unknown"} {
		rig, file := exampleRig(t, func(int) string { return "changes" })
		// Reviews 1 to 7 are the three scoped reviews and the full review of candidate 1 and the scoped reviews of candidate 2. Review 8 is the full review of candidate 2, the last.
		withCountSequence(rig, "0", append(slices.Repeat([]string{"0"}, 7), review)...)
		rig.setRound("fix_rounds", json.Number("1"))
		writer := newFakeWriter()
		rig.h.writer = writer

		_, err := runPipeline(rig.h.engine(t), file)

		require.NoError(t, err, review)
		assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses, review)
		assert.Equal(t, 5, countFix(rig), review)
		assert.Equal(t, append([]string{"opus", "sonnet"}, capModels(2)...), rig.models(), "plan, implement, the two candidates, and no settle lane for %q", review)
	}
}

func TestLaneLayout_TheSettleLaneIsTheOnlyEditAfterTheLastReviewAndRunsOnce(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	rig.setRound("fix_rounds", json.Number("2"))
	done := pipelineInBackground(rig.h.engine(t), file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "fix all"}))
	require.NoError(t, receive(t, done).err)

	requests := rig.h.agents.requests
	lastReview := -1

	for i, request := range requests {
		if strings.Contains(request.Prompt, reviewMarker) {
			lastReview = i
		}
	}

	require.Positive(t, lastReview)
	require.Len(t, requests, lastReview+2, "after the last review only the settle lane starts")
	assert.Contains(t, requests[lastReview+1].Prompt, "The owner's ruling")
	assert.NotContains(t, requests[lastReview+1].Prompt, "This is round")
}

// A resume reads the cap that the first start recorded: a changed vars.round.fix_rounds does not change the loop of a run that is half done.
func TestLaneLayout_AResumeKeepsTheCapOfTheFirstStartWhenTheFixRoundsChange(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	rig.setRound("fix_rounds", json.Number("3"))

	inner := rig.h.agents.result
	stopped := false

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)

		if !stopped && strings.Contains(request.Prompt, "This is round 2") {
			stopped = true

			if err := rig.h.client(t, testVersion).Cancel(testItem); err != nil {
				t.Error(err)
			}

			rig.h.tick(t)
		}

		return result
	}

	engine := rig.h.engine(t)
	_, err := runPipeline(engine, file)
	require.Error(t, err, "the run is canceled in the second fix lane")

	rig.setRound("fix_rounds", json.Number("1"))

	handle := resumeRun(t, engine, rig.h.client(t, testVersion))
	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	_, err = resultOfHandle(t, handle)
	require.NoError(t, err)

	rounds := map[string]bool{}

	for _, request := range rig.h.agents.requests {
		for _, round := range []string{"1", "2", "3", "4", "5"} {
			if strings.Contains(request.Prompt, "This is round "+round+".") {
				rounds[round] = true
			}
		}
	}

	// The fixes after candidate k are fix round k. Fix rounds 3 give four candidates, and the fixes inside candidate 1 are round 1 too.
	assert.Equal(t, map[string]bool{"1": true, "2": true, "3": true}, rounds, "the cap of 3 fix rounds holds after the resume, though vars.round.fix_rounds is 1 now")
}

func TestLaneLayout_TheLanesOfTheItemAreScopedReviewsAfterFixesAndOneFullReviewForEachGateThatPassed(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	})
	rig.h.writer = newFakeWriter()

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	lanes, err := rig.h.client(t, testVersion).Lanes(testItem)
	require.NoError(t, err)

	var kinds []string
	for _, lane := range lanes.Lanes {
		kinds = append(kinds, lane.Label)
	}

	assert.Equal(t, []string{"", "implement", "implement-sweep", "review 1/24", "fix 1/18", "review 2/24", "review 3/24"}, kinds,
		"a scoped review after the implement and after each fix, then the gate as proof, then one full review of the candidate")
	assert.Equal(t, 1, rig.h.gate.calls, "the scoped loop runs no gate")
}
