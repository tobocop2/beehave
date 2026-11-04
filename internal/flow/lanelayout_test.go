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

	"github.com/tobocop2/obee/internal/agent"
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

func TestLaneLayout_APatchRoundsCapOfTwoStartsTwoFixLanesAndThenAsksTheOwner(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	rig.h.opts.Env["PATCH_ROUNDS"] = "2"
	done := runInBackground2(rig.h.engine(t), file)

	waitForTheOwner(t, rig.h)
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "park all"))
	require.NoError(t, (<-done).err)

	assert.Equal(t, 2, countFix(rig), "two fix lanes for the pull request, then the owner rules")
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus", "sonnet"}, rig.models(), "plan, implement, review and fix twice, the third review, and the settle step")
}

func TestLaneLayout_TheDefaultCapIsFiveFixLanes(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	done := runInBackground2(rig.h.engine(t), file)

	waitForTheOwner(t, rig.h)
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "park all"))
	require.NoError(t, (<-done).err)

	assert.Equal(t, 5, countFix(rig))
}

func TestLaneLayout_APatchRoundsCapOutsideOneToFiveIsRefusedBeforeAnyAgent(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"0", "6", "-1", "many", "2.5"} {
		rig, file := exampleRig(t, func(int) string { return "approve" })
		rig.h.opts.Env["PATCH_ROUNDS"] = value

		_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

		require.Error(t, err, value)
		assert.Contains(t, err.Error(), "PATCH_ROUNDS", value)
		assert.Empty(t, rig.models(), "no agent starts for %q", value)
	}
}

func TestLaneLayout_ARiseInTheRegressionCountHoldsTheItemAndMergesNothing(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	withCounts(rig, "1", "2")
	rig.h.opts.Env["MERGE"] = "true"
	writer := newFakeWriter()
	rig.h.writer = writer

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Empty(t, result.Merge, "a rise blocks the merge")
	assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "regression count rose from 1 to 2")
}

func TestLaneLayout_ASameOrLowerRegressionCountDoesNotBlock(t *testing.T) {
	t.Parallel()

	for _, end := range []string{"1", "0"} {
		rig, file := exampleRig(t, func(int) string { return "approve" })
		withCounts(rig, "1", end)

		result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

		require.NoError(t, err)
		assert.Equal(t, "merged", result.Merge, "end count %s", end)
	}
}

// runHeld runs the example with a plan count and a review count that approve, and returns the result, the comments and the statuses.
func runHeld(t *testing.T, plan, review string) (ItemResult, *fakeWriter) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	withCounts(rig, plan, review)
	writer := newFakeWriter()
	rig.h.writer = writer

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)
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

		assert.Empty(t, result.Merge, name)
		assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses, name)
		require.Len(t, writer.notes, 1, name)
		assert.Contains(t, writer.notes[0], "no regression count that can be read", name)
	}
}

func TestLaneLayout_AnEqualCountMerges(t *testing.T) {
	t.Parallel()

	result, writer := runHeld(t, "1", "1")

	assert.Equal(t, "merged", result.Merge)
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

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, 1, countFix(rig))
	assert.Equal(t, "merged", result.Merge)
}

func TestLaneLayout_ARiseAtTheCapHoldsTheItemBeforeTheOwnerIsAskedAndNoSettleLaneStarts(t *testing.T) {
	t.Parallel()

	for _, review := range []string{"1", "unknown"} {
		rig, file := exampleRig(t, func(int) string { return "changes" })
		withCountSequence(rig, "0", "0", review)
		rig.h.opts.Env["PATCH_ROUNDS"] = "1"
		writer := newFakeWriter()
		rig.h.writer = writer

		_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

		require.NoError(t, err, review)
		assert.Equal(t, []string{testProject + "|" + testItem + "|hold"}, writer.statuses, review)
		assert.Equal(t, 1, countFix(rig), review)
		assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus"}, rig.models(), "plan, implement, review, one fix, the last review, and no settle lane for %q", review)
	}
}

func TestLaneLayout_TheSettleLaneIsTheOnlyEditAfterTheLastReviewAndRunsOnce(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	rig.h.opts.Env["PATCH_ROUNDS"] = "2"
	done := runInBackground2(rig.h.engine(t), file)

	waitForTheOwner(t, rig.h)
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "fix all"))
	require.NoError(t, (<-done).err)

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

// A resume reads the cap that the first start recorded: a changed PATCH_ROUNDS does not change the loop of a run that is half done.
func TestLaneLayout_AResumeKeepsTheCapOfTheFirstStartWhenPatchRoundsChanges(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	rig.h.opts.Env["PATCH_ROUNDS"] = "3"

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
	_, err := engine.RunFile(testItem, testProject, file)
	require.Error(t, err, "the run is canceled in the second fix lane")

	rig.h.opts.Env["PATCH_ROUNDS"] = "1"

	handle := resumeRun(t, engine, rig.h.client(t, testVersion))
	waitForTheOwner(t, rig.h)
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "park all"))
	_, err = joinRun(handle)
	require.NoError(t, err)

	rounds := map[string]bool{}

	for _, request := range rig.h.agents.requests {
		for _, round := range []string{"1", "2", "3"} {
			if strings.Contains(request.Prompt, "This is round "+round+".") {
				rounds[round] = true
			}
		}
	}

	assert.Equal(t, map[string]bool{"1": true, "2": true, "3": true}, rounds, "the cap of 3 holds after the resume, though PATCH_ROUNDS is 1 now")
}

func TestLaneLayout_TheLanesOfTheItemAreOneReviewForEachGateThatPassedAndFixLanesBetween(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	})
	rig.h.writer = newFakeWriter()

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	lanes, err := rig.h.client(t, testVersion).Lanes(testItem)
	require.NoError(t, err)

	var kinds []string
	for _, lane := range lanes.Lanes {
		kinds = append(kinds, lane.Kind)
	}

	assert.Equal(t, []string{"fix", "fix", "review", "rework", "review"}, kinds, "never two reviews in a row for one pull request: a fix lane, the gate as proof, then one review")
}
