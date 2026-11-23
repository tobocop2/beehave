package flow

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// A finding with no id is new. The ledger gives it f-1, and a reviewer that raises it again names f-1.
const (
	newFinding   = `[{"id":"","severity":"blocking","path":"a.go","text":"the retry never stops"}]`
	movedFinding = `[{"id":"f-1","severity":"blocking","path":"b.go","text":"the retry never stops, now in b.go"}]`
	wontfixOfOne = `[{"id":"f-1","reason":"the cost is higher than the gain"}]`
)

// changesThrough is the verdict of a review: changes up to the review `last`, then approve.
func changesThrough(last int) func(int) string {
	return func(review int) string {
		if review <= last {
			return "changes"
		}

		return "approve"
	}
}

// findingsOf is the findings that each review writes: the text of its number, and the empty list for any other review.
func findingsOf(byReview map[int]string) func(int) string {
	return func(review int) string {
		if text, ok := byReview[review]; ok {
			return text
		}

		return "[]"
	}
}

// requestsWith is the requests of the agents of the example rig whose prompt holds the marker, in order.
func requestsWith(rig *agentRig, marker string) []AgentRequest {
	var found []AgentRequest

	for _, request := range rig.h.agents.requests {
		if strings.Contains(request.Prompt, marker) {
			found = append(found, request)
		}
	}

	return found
}

// wontfixOnTheFirstFix makes the first fix agent of the rig write the output wontfix. The other outputs of a fix stay clean.
func wontfixOnTheFirstFix(rig *agentRig, wontfix string) {
	inner := rig.h.agents.result

	var fixes atomic.Int64

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)

		if strings.Contains(request.Prompt, fixMarker) && fixes.Add(1) == 1 {
			rig.putOutputs(request.OutputFile, map[string]string{"report": cleanLaneReport("fix"), "uncaused": "[]", "resolved": "[]", "wontfix": wontfix})
		}

		return result
	}
}

// ownerQuestion waits for the question that the open run shows to the owner, and returns its text.
func ownerQuestion(t *testing.T, h *harness) waiting[string] {
	t.Helper()

	client := h.client(t, testVersion)
	read := newStatusRead(client)

	return waiting[string]{what: "the open run to show a question", run: runOfClient(t, client), last: read.last, read: func() (string, bool, error) {
		infos := read.next()

		if read.err == nil && len(infos) == 1 && infos[0].Question != "" {
			return infos[0].Question, true, nil
		}

		return "", false, nil
	}}
}

func TestExample_AFindingThatTheNextReviewRaisesAgainKeepsItsIdAcrossTwoReviewRounds(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, changesThrough(2), findingsOf(map[int]string{1: newFinding, 2: movedFinding}))

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)

	reviews := requestsWith(rig, reviewMarker)
	require.GreaterOrEqual(t, len(reviews), 3, "two reviews that ask for changes, then the review that approves")

	second, text := readLedger(reviews[1].DataFile)
	require.Len(t, second, 1, "the second review sees the finding of the first")
	assert.Equal(t, "f-1", second[0].ID)
	assert.Equal(t, "open", second[0].State)
	assert.Contains(t, text, "base_reds", "the data of the lane stays in the file beside the ledger")

	third, _ := readLedger(reviews[2].DataFile)
	require.Len(t, third, 1, "the id is kept: the finding is not a second entry")
	assert.Equal(t, ledgerEntry{ID: "f-1", Path: "b.go", Severity: "blocking", Text: "the retry never stops, now in b.go", State: "open", RaisedIn: 1, LastRaised: 2}, third[0])
}

func TestExample_AFindingThatTheNextReviewOmitsIsFixedInTheLedgerOfTheLaterReviews(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, changesThrough(1), findingsOf(map[int]string{1: newFinding}))

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)

	reviews := requestsWith(rig, reviewMarker)
	require.GreaterOrEqual(t, len(reviews), 3, "the review that raised it, the review that omitted it and the full review")

	last, _ := readLedger(reviews[len(reviews)-1].DataFile)
	require.Len(t, last, 1)
	assert.Equal(t, "fixed", last[0].State, "the review before the full review did not list f-1")
}

func TestExample_AFixGetsTheOpenFindingsOfTheLedgerInItsDataFile(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, changesThrough(1), findingsOf(map[int]string{1: newFinding}))

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)

	fixes := requestsWith(rig, fixMarker)
	require.NotEmpty(t, fixes)

	seen, _ := readLedger(fixes[0].DataFile)
	require.Len(t, seen, 1)
	assert.Equal(t, "f-1", seen[0].ID)
	assert.Equal(t, "open", seen[0].State)
}

func TestExample_AFindingThatTheFixDeclaresWontfixReachesTheOwnerWithItsReasonAtTheEndOfTheLoop(t *testing.T) {
	t.Parallel()

	rig, file, _ := stopLoopRig(t, 0, nil, false)
	rig.setRound("fix_rounds", oneFixRound)
	rig.answerLikeTheExample(exampleSkill, alwaysChanges, findingsOf(map[int]string{1: newFinding}), func(int) string { return "" }, "", capReviews)
	wontfixOnTheFirstFix(rig, wontfixOfOne)
	done := pipelineInBackground(rig.h.engine(t), file)

	question := waitOn(t, ownerQuestion(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	outcome := receive(t, done)

	require.NoError(t, outcome.err)
	assert.Contains(t, question, "f-1", "the question names the finding")
	assert.Contains(t, question, "the cost is higher than the gain", "and the reason of the fix")
	assert.Contains(t, question, "a.go", "and its file")
}

func TestExample_AnApprovedItemGetsTheWontfixFindingsOfTheLedgerInItsApprovalComment(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, changesThrough(1), findingsOf(map[int]string{1: newFinding}))
	wontfixOnTheFirstFix(rig, wontfixOfOne)

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.notes, 1)
	assert.Contains(t, rig.h.forge.notes[0], "f-1", "the comment names the finding that the review accepted as wontfix")
	assert.Contains(t, rig.h.forge.notes[0], "the cost is higher than the gain")
}

func TestExample_AnApprovedItemWithNoWontfixFindingGetsNoWontfixListInItsApprovalComment(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, changesThrough(1), findingsOf(map[int]string{1: newFinding}))

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.notes, 1)
	assert.NotContains(t, rig.h.forge.notes[0], "wontfix", "a fixed finding is not a wontfix")
}

func TestExample_TheReviewPromptAsksForFindingsAsJSONTextWithLedgerIdsAndTheFixPromptForResolvedAndWontfix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, changesThrough(1), findingsOf(map[int]string{1: newFinding}))

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)

	review := requestsWith(rig, reviewMarker)[0].Prompt
	assert.Contains(t, review, "`ledger`", "the review reads the ledger in its data file")
	assert.Contains(t, review, "`severity`", "each finding has a severity")
	assert.NotContains(t, review, `"class":`, "the findings are no array of class and text objects")

	fixes := requestsWith(rig, fixMarker)
	require.NotEmpty(t, fixes)
	assert.Contains(t, fixes[0].Prompt, "`resolved`")
	assert.Contains(t, fixes[0].Prompt, "`wontfix`")
	assert.Contains(t, fixes[0].Prompt, "`ledger`", "the fix reads the open findings in its data file")
}
