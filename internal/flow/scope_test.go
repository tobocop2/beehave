package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
)

// gateThenMerge gates the item once, then merges.
func gateThenMerge(gates int) string {
	return bodyFlow(strings.Repeat("gate(name = \"g\")\n", gates) + shipCall("merge = True, "))
}

func TestScope_ANewClosedIssueWithoutAnOwnerLineRefusesTheMergeAndNamesTheIssueAndTheAnswer(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.closing = [][]int{{12}, {12, 13}}
	h.flowIs(t, gateThenMerge(1))
	engine := h.engine(t)
	done := runInBackground(engine, testItem)

	waitForTheOwner(t, h)
	require.NoError(t, h.client(t, testVersion).Answer(testItem, "no"))

	err := (<-done).err

	require.ErrorIs(t, err, errScopeRefused)
	require.ErrorContains(t, err, "#13 has no owner line")
	require.ErrorContains(t, err, "obee answer 12 \"keep #13\"")
	assert.Empty(t, h.merge.asked, "nothing merges")
}

func TestScope_AnOwnerLineThatNamesTheNewIssueLetsTheMergeGo(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.closing = [][]int{{12}, {13, 12, 13}}
	h.flowIs(t, gateThenMerge(1))
	done := runInBackground(h.engine(t), testItem)

	waitForTheOwner(t, h)
	require.NoError(t, h.client(t, testVersion).Answer(testItem, "keep #13"))

	require.NoError(t, (<-done).err)
	assert.Len(t, h.merge.asked, 1)
}

func TestScope_AnIssueThatLeftTheSetNeedsNoQuestion(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.closing = [][]int{{12, 13}, {12}}

	_, err := walkText(t, h, gateThenMerge(1))

	require.NoError(t, err)
	assert.Len(t, h.merge.asked, 1)
}

func TestScope_ALaterGateKeepsTheScopeOfTheFirstAndReadsNothing(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.closing = [][]int{{12}}

	_, err := walkText(t, h, gateThenMerge(3))

	require.NoError(t, err)
	assert.Equal(t, 2, h.forge.closingReads, "one read at the first gate and one before the merge")
}

func TestScope_AGateBeforeThePullRequestExistsFixesNoScope(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.noPull = true

	_, err := walkText(t, h, bodyFlow("gate(name = \"g\")\n"))

	require.NoError(t, err)
	assert.Equal(t, 1, h.forge.closingReads)
}

func TestScope_AMergeWithNoGateHasNoScopeToCompareAndReadsNothing(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.closing = [][]int{{12, 13}}

	_, err := walkText(t, h, shipFlow("merge = True, "))

	require.NoError(t, err)
	assert.Len(t, h.merge.asked, 1)
	assert.Zero(t, h.forge.closingReads)
}

func TestScope_AFailedReadBeforeTheMergeRefusesAndNothingMerges(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.closing = [][]int{{12}}
	h.forge.closingErr, h.forge.closingOK = errFake, 1

	_, err := walkText(t, h, gateThenMerge(1))

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "scope-check of item 12")
	assert.Empty(t, h.merge.asked)
}

func TestScope_AFailedReadAtTheFirstGateEndsTheItem(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.closingErr = errFake

	_, err := walkText(t, h, gateThenMerge(1))

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "scope of item 12")
}

func TestScope_APullRequestThatIsGoneBeforeTheMergeRefuses(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.closing = [][]int{{12}}
	h.forge.noPull, h.forge.noPullAfter = true, 1

	_, err := walkText(t, h, gateThenMerge(1))

	require.ErrorContains(t, err, "the item has no pull request")
	assert.Empty(t, h.merge.asked)
}

func TestScope_AnOwnerMergeIsNotChecked(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.closing = [][]int{{12}, {12, 13}}

	_, err := walkText(t, h, bodyFlow("gate(name = \"g\")\n"+shipCall("")))

	require.NoError(t, err)
	assert.Equal(t, 1, h.forge.closingReads, "only the first gate reads")
}

func TestScope_AQuestionThatCannotBeShownEndsTheAsk(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))
	s := &script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}, fixed: true, scope: []int{12}}

	err := s.askScope([]int{13})

	require.ErrorContains(t, err, "item 12 cannot show its question")
}

// answerTheScopeQuestion runs gates then a merge over the closing sets, answers the one question with answer, and returns the error of the run.
func answerTheScopeQuestion(t *testing.T, closing [][]int, gates int, answer string) (*harness, error) {
	t.Helper()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.closing = closing
	h.flowIs(t, gateThenMerge(gates))
	done := runInBackground(h.engine(t), testItem)

	waitForTheOwner(t, h)
	require.NoError(t, h.client(t, testVersion).Answer(testItem, answer))

	return h, (<-done).err
}

func TestScope_AnAnswerThatNamesAnotherNumberThatStartsWithTheIssueRefuses(t *testing.T) {
	t.Parallel()

	h, err := answerTheScopeQuestion(t, [][]int{{12}, {12, 13}}, 1, "keep #130")

	require.ErrorIs(t, err, errScopeRefused)
	require.ErrorContains(t, err, "#13 has no owner line")
	assert.Empty(t, h.merge.asked)
}

func TestScope_AnAnswerThatIsNotTheKeepFormRefusesAndShowsTheForm(t *testing.T) {
	t.Parallel()

	for _, answer := range []string{"no, do not keep #13", "stop #13", "13", "keep 13 please", "keep"} {
		h, err := answerTheScopeQuestion(t, [][]int{{12}, {12, 13}}, 1, answer)

		require.ErrorIs(t, err, errScopeRefused, answer)
		require.ErrorContains(t, err, "keep #N", answer)
		assert.Empty(t, h.merge.asked, answer)
	}
}

func TestScope_TheKeepFormIsCaseInsensitiveAndTakesAList(t *testing.T) {
	t.Parallel()

	h, err := answerTheScopeQuestion(t, [][]int{{12}, {12, 13, 14}}, 1, " KEEP #13, #14 ")

	require.NoError(t, err)
	assert.Len(t, h.merge.asked, 1)
}

func TestScope_AnAnswerThatNamesOneOfTwoNewIssuesRefusesAndNamesTheOther(t *testing.T) {
	t.Parallel()

	h, err := answerTheScopeQuestion(t, [][]int{{12}, {12, 13, 14}}, 1, "keep #13")

	require.ErrorIs(t, err, errScopeRefused)
	require.ErrorContains(t, err, "#14 has no owner line")
	assert.NotContains(t, err.Error(), "#13 has")
	assert.Empty(t, h.merge.asked)
}

func TestScope_ASetThatChangesBetweenGatesIsComparedWithTheFirstGate(t *testing.T) {
	t.Parallel()

	h, err := answerTheScopeQuestion(t, [][]int{{12}, {12, 13}, {12, 13}}, 2, "no")

	require.ErrorIs(t, err, errScopeRefused)
	require.ErrorContains(t, err, "#13 has no owner line")
	assert.Empty(t, h.merge.asked)
}

func TestKeptIssues_ReadsOnlyTheKeepForm(t *testing.T) {
	t.Parallel()

	kept, ok := keptIssues("keep #13, #14")
	assert.True(t, ok)
	assert.Equal(t, []int{13, 14}, kept)

	_, ok = keptIssues("keep #99999999999999999999")
	assert.False(t, ok)
}

func TestIssueList_NamesEachIssueOrNoIssue(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "no issue", issueList(nil))
	assert.Equal(t, "#12 #13", issueList([]int{12, 13}))
}
