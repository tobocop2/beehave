package flow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	trackerstore "github.com/tobocop2/beehave/internal/store"
)

// These tests run the review loop of the example and read what it does with the outputs that a lane writes.

// laneReportCheck is the step that checks the report of a lane.
const laneReportCheck = "command:candidate-lane-report#3"

// reportRuns returns the requests of the example rig that ran `remedy report`.
func reportRuns(rig *agentRig) []CommandRequest {
	rig.h.commands.mu.Lock()
	defer rig.h.commands.mu.Unlock()

	var runs []CommandRequest

	for _, request := range rig.h.commands.requests {
		if len(request.Run) > 1 && request.Run[0] == "remedy" && request.Run[1] == "report" {
			runs = append(runs, request)
		}
	}

	return runs
}

// replyToReports answers the calls of `remedy report` in order, and the last answer repeats. Every other command gets an empty success.
func replyToReports(rig *agentRig, outputs ...CommandOutput) {
	next := 0

	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) < 2 || request.Run[0] != "remedy" || request.Run[1] != "report" {
			return CommandOutput{}, nil
		}

		out := outputs[min(next, len(outputs)-1)]
		next++

		return out, nil
	}
}

// reportCall is the run line that the check of a lane report must have for the head.
func reportCall(head string) []string {
	return []string{"remedy", "report", "/dev/stdin", "--head", head}
}

// agentIndex returns the place, among the agent requests of the rig, of the first request whose prompt holds the phrase. It is -1 for none.
func agentIndex(rig *agentRig, phrase string) int {
	for i, request := range rig.h.agents.requests {
		if strings.Contains(request.Prompt, phrase) {
			return i
		}
	}

	return -1
}

func TestExample_TheReportOfTheImplementLaneIsCheckedAtTheHeadOfTheLane(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "approve" })
	rig := newCommittingRig(t, base, implementMarker, fixMarker)

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)
	require.Len(t, rig.heads, 1, "only the implement step committed")

	runs := reportRuns(rig.agentRig)
	require.Len(t, runs, 1, "the clean implementer makes one candidate, and its report is checked once")
	assert.Equal(t, reportCall(rig.heads[0]), runs[0].Run, "the check judges the report against the head of the implementer")
	assert.Equal(t, cleanLaneReport("implement"), runs[0].Stdin, "the report that the implement step wrote is the stdin of the check")
}

func TestExample_TheReportOfTheLastFixIsCheckedAtTheHeadOfThatFix(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, approveAfter(1))
	rig := newCommittingRig(t, base, implementMarker, fixMarker)

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)
	require.Len(t, rig.heads, 2, "the implement step and one fix committed")

	runs := reportRuns(rig.agentRig)
	require.Len(t, runs, 1, "the candidate comes from the fix, so the report of the fix is the one that is checked")
	assert.Equal(t, reportCall(rig.heads[1]), runs[0].Run, "the check names the head of the fix, not the head of the implementer")
	assert.Equal(t, cleanLaneReport("fix"), runs[0].Stdin, "the report that the fix wrote is the stdin of the check")
}

func TestExample_ALaneReportWithAMissingTableIsSentBackBeforeTheFullReview(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "approve" })
	rig := newCommittingRig(t, base, implementMarker, fixMarker)
	replyToReports(rig.agentRig, CommandOutput{RC: 1, Stdout: "no ## Hunks section\n"}, CommandOutput{RC: 0})

	result, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)
	require.Len(t, rig.heads, 2, "the implement step and the fix that the defect sent back")

	fixes := fixPrompts(rig.agentRig)
	require.Len(t, fixes, 1, "the defect sends the lane back once")
	assert.Contains(t, fixes[0], "The report of the lane has a defect. Each line is one finding:\nno ## Hunks section", "the lines of the tool are the findings of the fix")
	assert.Less(t, agentIndex(rig.agentRig, fixMarker), agentIndex(rig.agentRig, fullReview), "no full review ran before the fix: the defective head is never judged")
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "opus"}, rig.models(), "the plan, the implement step, its scoped review, the fix, its scoped review, then the one full review of the fixed head")

	runs := reportRuns(rig.agentRig)
	require.Len(t, runs, 2)
	assert.Equal(t, reportCall(rig.heads[0]), runs[0].Run)
	assert.Equal(t, reportCall(rig.heads[1]), runs[1].Run, "the second candidate is checked at the head of the fix")
	assert.True(t, childOf(t, result).Approved)
}

func TestExample_ACleanLaneReportAddsNoFixRoundAndNoReview(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "approve" })
	rig := newCommittingRig(t, base, implementMarker, fixMarker)
	replyToReports(rig.agentRig, CommandOutput{RC: 0, Stdout: "OK: the report is whole\n"})

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	assert.Equal(t, []string{"opus", "sonnet", "opus", "opus"}, rig.models(), "the base list of agent calls: the plan, the implement step, its scoped review and the full review")
	assert.Empty(t, fixPrompts(rig.agentRig), "a clean report sends nothing back")
	assert.Len(t, reportRuns(rig.agentRig), 1)
}

func TestExample_AReportCheckThatCannotRunStopsTheItem(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "approve" })
	replyToReports(base, CommandOutput{RC: 2, Stdout: "remedy report: cannot read the report\n"})

	result, err := runPipeline(base.h.engine(t), file)
	require.NoError(t, err)

	require.NotNil(t, result.Stopped, "the check that cannot run ends the item")
	assert.Equal(t, laneReportCheck, result.Stopped.Step)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, base.models(), "the plan, the implement step and its scoped review ran, and no full review started")
	assert.Equal(t, -1, agentIndex(base, fullReview))
}

func TestExample_AReportCheckExitOtherThanZeroOneOrTwoIsADefectOfTheLane(t *testing.T) {
	t.Parallel()

	for name, rc := range map[string]int{"a crash": 3, "a missing binary": 127} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base, file := exampleRig(t, func(int) string { return "approve" })
			rig := newCommittingRig(t, base, implementMarker, fixMarker)
			replyToReports(rig.agentRig, CommandOutput{RC: rc, Stdout: "remedy: no such command\n"}, CommandOutput{RC: 0})

			result, err := runPipeline(rig.h.engine(t), file)
			require.NoError(t, err)

			assert.Nil(t, result.Stopped, "only exit code 2 ends the item")

			fixes := fixPrompts(rig.agentRig)
			require.Len(t, fixes, 1, "the exit code counts as a defect and sends the lane back once")
			assert.Contains(t, fixes[0], "The report of the lane has a defect. Each line is one finding:\nremedy: no such command")
			assert.Len(t, reportRuns(rig.agentRig), 2, "the fixed head is checked again")
		})
	}
}

// orderWriter is a tracker writer that notes whether the approval comment of the pull request was posted before a Create call.
type orderWriter struct {
	*fakeWriter

	forge         *fakeForge
	approvedFirst bool
}

func (w *orderWriter) Create(ctx context.Context, project, key string, item trackerstore.NewItem) (string, bool, error) {
	if len(approvals(w.forge)) > 0 {
		w.approvedFirst = true
	}

	return w.fakeWriter.Create(ctx, project, key, item)
}

// approvals returns the approval comments that the fake forge holds.
func approvals(forge *fakeForge) []string {
	forge.mu.Lock()
	defer forge.mu.Unlock()

	var notes []string

	for _, note := range forge.notes {
		if strings.Contains(note, ":approved|") {
			notes = append(notes, note)
		}
	}

	return notes
}

// uncausedEntry is the entry of the output uncaused for the red `id`.
func uncausedEntry(id string) string {
	return `{"id":"` + id + `","title":"The red ` + id + `","body":"## Problem\nIt is red.","found":"Run make check at commit abc.","severity":"P2","operator":false}`
}

// uncausedRig is the committing example rig with a tracker writer, whose lanes write the output uncaused. byLane gives the list of a lane kind (implement or fix), and a kind with no entry writes [].
func uncausedRig(t *testing.T, base *agentRig, byLane map[string]string) (*committingRig, *orderWriter) {
	t.Helper()

	rig := newCommittingRig(t, base, implementMarker, fixMarker)
	writer := &orderWriter{fakeWriter: newFakeWriter(), forge: rig.h.forge}
	rig.h.writer = writer

	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		done := inner(request)

		kind := laneKind(request)
		if list, set := byLane[kind]; set && slices.Contains(request.Outputs, "uncaused") {
			rig.putOutputs(request.OutputFile, map[string]string{"report": cleanLaneReport(kind), "uncaused": list, "resolved": "[]", "wontfix": "[]"})
		}

		return done
	}

	return rig, writer
}

func TestExample_AGateRedTheLaneDidNotCauseFilesARepairItemBeforeTheMerge(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, approveAfter(1))
	rig, writer := uncausedRig(t, base, map[string]string{"fix": "[" + uncausedEntry("retry-loop") + "]"})

	result, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	require.Len(t, writer.created, 1, "the fix lane reported one red, so one item is filed")
	assert.True(t, strings.HasPrefix(writer.created[0], testProject+"|"+testRun+":finding-retry-loop|The red retry-loop|"), "the key is the id of the red")
	assert.False(t, writer.approvedFirst, "the item exists before the approval comment")
	assert.Len(t, approvals(rig.h.forge), 1, "the review approved the head")
	assert.True(t, childOf(t, result).Approved)
}

func TestExample_ARedThatTwoLanesReportIsFiledOnce(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		implement, fix string
		keys           []string
	}{
		"the same id from both lanes": {uncausedEntry("one-red"), uncausedEntry("one-red"), []string{"finding-one-red"}},
		"two ids from two lanes":      {uncausedEntry("red-a"), uncausedEntry("red-b"), []string{"finding-red-a", "finding-red-b"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base, file := exampleRig(t, approveAfter(1))
			rig, writer := uncausedRig(t, base, map[string]string{"implement": "[" + tc.implement + "]", "fix": "[" + tc.fix + "]"})

			_, err := runPipeline(rig.h.engine(t), file)
			require.NoError(t, err)

			var keys []string

			for _, created := range writer.created {
				keys = append(keys, strings.TrimPrefix(strings.Split(created, "|")[1], testRun+":"))
			}

			assert.Equal(t, tc.keys, keys)
		})
	}
}

func TestExample_AFailedRepairFilingLeavesTheItemUnapprovedAndPostsNoApproval(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "approve" })
	rig, writer := uncausedRig(t, base, map[string]string{"implement": "[" + uncausedEntry("retry-loop") + "]"})
	writer.err = errors.New("tracker down")

	_, err := runPipeline(rig.h.engine(t), file)

	require.Error(t, err, "the refused filing ends the item with an error")
	assert.Empty(t, writer.created)
	assert.Empty(t, approvals(rig.h.forge), "no approval exists for an item whose repair item is not filed")
}

func TestExample_ALaneWithNoUncausedRedFilesNoItem(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, approveAfter(1))
	rig, writer := uncausedRig(t, base, map[string]string{"implement": "[]", "fix": "[]"})

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	assert.Empty(t, writer.created, "an empty list from every lane makes no item")
	assert.Len(t, approvals(rig.h.forge), 1)
}

func TestExample_AnUncausedListWithABadEntryFilesNothingAndRefusesTheApproval(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ list, refusal string }{
		"an entry with no found": {"[" + uncausedEntry("good-red") + `,{"id":"bad-red","title":"t","body":"b","severity":"P2","operator":false}]`, `an entry of uncaused has no "found"`},
		"a list that is no list": {uncausedEntry("good-red"), "the output uncaused of a lane is not a JSON list"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base, file := exampleRig(t, func(int) string { return "approve" })
			rig, writer := uncausedRig(t, base, map[string]string{"implement": tc.list})

			_, err := runPipeline(rig.h.engine(t), file)

			require.ErrorContains(t, err, tc.refusal)
			assert.Empty(t, writer.created, "the good entry before the bad one is not filed")
			assert.Empty(t, approvals(rig.h.forge))
		})
	}
}

func TestExample_AnUncausedListThatIsNotJSONIsRefusedWithTheListAndTheLane(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		lists   map[string]string
		refusal string
	}{
		"the implement lane": {map[string]string{"implement": "not json at all"}, `REFUSE: the output uncaused of the implement lane is not valid JSON: "not json at all".`},
		"a fix lane":         {map[string]string{"implement": "[]", "fix": "[oops"}, `REFUSE: the output uncaused of fix lane 1 is not valid JSON: "[oops".`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base, file := exampleRig(t, approveAfter(1))
			rig, writer := uncausedRig(t, base, tc.lists)

			_, err := runPipeline(rig.h.engine(t), file)

			require.ErrorContains(t, err, tc.refusal)
			assert.Contains(t, err.Error(), "Write [] when there is nothing to report", "the refusal says what to write")
			assert.NotContains(t, err.Error(), "json.decode", "the raw decoder error is not what the item ends with")
			assert.Empty(t, writer.created)
			assert.Empty(t, approvals(rig.h.forge))
		})
	}
}
