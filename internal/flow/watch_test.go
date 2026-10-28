package flow

import (
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/review"
)

func TestRun_TheWatchStepEndsOnMergeAndKeepsTheDraftFlag(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.watch.outcomes = []review.Outcome{{Kind: review.Waiting}, {Kind: review.Waiting}, {Kind: review.Merged}}
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)

	reads, drafts := harness.watch.seen()
	assert.Equal(t, []int{testPull, testPull, testPull}, reads, "the step reads again after each durable sleep until the merge")
	assert.Empty(t, drafts)
	assert.Equal(t, []string{"claim:12", "ready:12", "release:12"}, harness.life.seen())
}

func TestRun_TheWatchStepSendsAPullRequestBackToDraftAndEnds(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"a_conflict":       "it conflicts with its base branch",
		"a_change_request": "a review asks for changes",
	}

	for name, reason := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.watch.outcomes = []review.Outcome{{Kind: review.Waiting}, {Kind: review.SendBack, Reason: reason}}
			engine := harness.engine(t)

			result, err := engine.Run(testItem, testProject)

			require.NoError(t, err, "a send back is a finished run")
			assert.Equal(t, ItemResult{Item: testItem, SentBack: reason}, result)

			reads, drafts := harness.watch.seen()
			assert.Equal(t, []int{testPull, testPull}, reads, "the step ends at the send back")
			assert.Equal(t, []int{testPull}, drafts)
			assert.Equal(t, []string{"claim:12", "ready:12", "release:12"}, harness.life.seen())
		})
	}
}

func TestRun_ASentBackItemStartsANewAttemptAndStatusShowsTheReason(t *testing.T) {
	t.Parallel()

	const reason = "a review asks for changes"

	harness := newHarness(t)
	harness.watch.outcomes = []review.Outcome{{Kind: review.SendBack, Reason: reason}, {Kind: review.Merged}}
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, RunInfo{ID: "item-12-r1", State: runDone, Version: testVersion, SentBack: reason}, infos[0])
	require.ErrorIs(t, client.Resume(testItem), errWrongState, "a sent-back run is finished, so it does not resume")

	result, err := engine.Run(testItem, testProject)
	require.NoError(t, err)
	assert.Equal(t, ItemResult{Item: testItem}, result)

	infos, err = client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 2)
	assert.Equal(t, "item-12-r2", infos[1].ID, "the second run is attempt 2")
}

func TestSentBackOf_ReadsTheResultOfAFinishedRun(t *testing.T) {
	t.Parallel()

	result, err := resultOf(dbos.WorkflowStatus{ID: "item-12-r1"})
	require.NoError(t, err)
	assert.Empty(t, result.SentBack, "a run with no stored result has no reason")

	result, err = resultOf(dbos.WorkflowStatus{ID: "item-12-r1", Output: `{"item":"12","sent_back":"it conflicts","merge":"sent_back"}`})
	require.NoError(t, err)
	assert.Equal(t, "it conflicts", result.SentBack)
	assert.Equal(t, "sent_back", result.Merge)

	_, err = resultOf(dbos.WorkflowStatus{ID: "item-12-r1", Output: "not json"})
	require.ErrorContains(t, err, "read the result of item-12-r1")
}

func TestWatchEvery_IsOneHour(t *testing.T) {
	t.Parallel()

	assert.Equal(t, time.Hour, watchEvery, "a day of waiting costs 48 durable reads")
}

func TestRun_TheWatchStepEndsWithTheLastErrorAfterThreeFailedReadsInARow(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.watch.outcomes = []review.Outcome{{Kind: review.Waiting}}
	harness.watch.err = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)

	reads, drafts := harness.watch.seen()
	assert.Len(t, reads, 3)
	assert.Empty(t, drafts, "a read that failed does not send the pull request back")
}

func TestRun_AGoodReadResetsTheCountOfFailedReads(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.watch.outcomes = []review.Outcome{{Kind: review.Waiting}, {Kind: review.Waiting}, {Kind: review.Waiting}, {Kind: review.Waiting}, {Kind: review.Waiting}, {Kind: review.Merged}}
	harness.watch.errs = []error{errFake, errFake, nil, errFake, errFake, nil}
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)

	reads, _ := harness.watch.seen()
	assert.Len(t, reads, 6)
}

func TestRun_TwoFailedReadsThenAMergeEndWithoutAnError(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.watch.outcomes = []review.Outcome{{Kind: review.Waiting}, {Kind: review.Waiting}, {Kind: review.Merged}}
	harness.watch.errs = []error{errFake, errFake, nil}
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
}

func TestRun_TheWatchStepEndsWithAnErrorWhenTheNumberOfThePullRequestCannotBeRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.forge.failFrom = 2
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "watch of item 12")

	reads, _ := harness.watch.seen()
	assert.Empty(t, reads)
}

func TestRun_TheWatchStepEndsWithAnErrorWhenTheDraftCallFails(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.watch.outcomes = []review.Outcome{{Kind: review.SendBack, Reason: "it conflicts with its base branch"}}
	harness.watch.draftErr = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)
}

func TestCancel_AWatchThatSleepsEndsWithTheCancel(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.sleep = time.Hour
	harness.watch.outcomes = []review.Outcome{{Kind: review.Waiting}}
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { reads, _ := harness.watch.seen(); return len(reads) == 1 })

	require.NoError(t, client.Cancel(testItem))
	harness.tick(t)

	select {
	case outcome := <-done:
		require.Error(t, outcome.err)
	case <-time.After(eventTimeout):
		t.Fatal("the run did not end after the cancel")
	}
}

func TestRun_TheWatchStepDoesNotStartAfterARefusedReady(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.forge.state = review.Standing{Number: testPull, Conflicting: true}
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errNotReady)

	reads, _ := harness.watch.seen()
	assert.Empty(t, reads)
}
