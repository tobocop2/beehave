package flow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
)

// stoppedInTheWait runs an item into the wait of a queued merge, stops the engine there, and returns the harness. The engine stop keeps the merge armed.
func stoppedInTheWait(t *testing.T) *harness {
	t.Helper()

	harness, _ := queuedHarness(t, merge.Result{Stop: merge.Refused, Reason: merge.Queued})
	harness.sleep = time.Minute

	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.merge.askedCount() == 1 })
	require.NoError(t, engine.Shutdown())
	<-done

	require.Empty(t, harness.merge.snapshot(), "an engine stop keeps the queued merge armed")

	return harness
}

func TestCancel_WithNoEngineTurnsOffTheQueuedMerge(t *testing.T) {
	t.Parallel()

	harness := stoppedInTheWait(t)
	client := harness.client(t, testVersion)

	require.NoError(t, client.Cancel(testItem))

	assert.Equal(t, []string{testItem}, harness.merge.snapshot(), "no live wait is left to turn it off, so the cancel does")
}

func TestCancel_AnEngineThatStartsLaterTurnsOffTheQueuedMergeOfACancelItMissed(t *testing.T) {
	t.Parallel()

	harness := stoppedInTheWait(t)
	client := harness.client(t, testVersion)

	held, err := holdEngine(client.dir) // An engine that holds the lock leaves the release to itself, and the marker stays.
	require.NoError(t, err)
	require.NoError(t, client.Cancel(testItem))
	require.Empty(t, harness.merge.snapshot())
	require.NoError(t, held.Unlock())

	harness.engine(t)

	eventually(t, func() bool { return len(harness.merge.snapshot()) == 1 })
	assert.Equal(t, []string{testItem}, harness.merge.snapshot())
}

func TestCancel_AnEngineThatCannotTurnOffTheQueuedMergeOfAMissedCancelStillGivesTheItemBack(t *testing.T) {
	t.Parallel()

	harness := stoppedInTheWait(t)
	harness.merge.disarmErr = errFake
	client := harness.client(t, testVersion)

	held, err := holdEngine(client.dir)
	require.NoError(t, err)
	require.NoError(t, client.Cancel(testItem))
	require.NoError(t, held.Unlock())

	harness.engine(t)

	eventually(t, func() bool { return countOf(harness.life.seen(), "release") == 1 })
	assert.Equal(t, []string{testItem}, harness.merge.snapshot(), "the disarm was tried once, and its failure did not stop the give-back")
}

// rowOf builds the stored row of a child workflow whose input is in.
func rowOf(t *testing.T, id string, in ExclusiveInput) dbos.WorkflowStatus {
	t.Helper()

	data, err := json.Marshal(in)
	require.NoError(t, err)

	return dbos.WorkflowStatus{ID: id, Input: string(data)}
}

func TestDisarmCanceled_ReadsTheMergesOfTheRunAndSkipsWhatIsNotAMerge(t *testing.T) {
	t.Parallel()

	fake := &fakeMerge{}
	rows := []dbos.WorkflowStatus{
		rowOf(t, "gate", ExclusiveInput{Kind: kindGate, Item: "1"}),
		rowOf(t, "merge", ExclusiveInput{Kind: kindMerge, Project: testProject, Item: "2", Head: gatedHead}),
	}

	err := disarmCanceled(t.Context(), func(...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) { return rows, nil }, fake, "run-2")

	require.NoError(t, err)
	assert.Equal(t, []string{"2"}, fake.disarms)
}

func TestDisarmCanceled_AFailureIsAnErrorThatSaysTheMergeMayStillBeArmed(t *testing.T) {
	t.Parallel()

	fake := &fakeMerge{disarmErr: errFake}
	rows := []dbos.WorkflowStatus{{ID: "bad", Input: "not json"}, rowOf(t, "merge", ExclusiveInput{Kind: kindMerge, Item: "2"})}

	err := disarmCanceled(t.Context(), func(...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) { return rows, nil }, fake, "run-2")

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the input of the merge bad")
	require.ErrorContains(t, err, "queued merge of item 2 may still be armed")
}

func TestDisarmCanceled_AListThatFailsIsAnErrorAndANilMergerTurnsNothingOff(t *testing.T) {
	t.Parallel()

	fail := func(...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) { return nil, errFake }

	require.ErrorIs(t, disarmCanceled(t.Context(), fail, &fakeMerge{}, "run-2"), errFake)
	require.NoError(t, disarmCanceled(context.Background(), fail, nil, "run-2"))
}
