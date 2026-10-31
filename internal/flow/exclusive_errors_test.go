package flow

import (
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExclusive_ARecordedRunOfAnUnknownKindFailsWithoutRunningAnything(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	var out ExclusiveOutput

	require.ErrorContains(t, engine.exclusiveCall(nil, ExclusiveInput{Kind: "other"}, &out), `an exclusive run has no kind "other"`)

	assert.Empty(t, out.Outputs)
}

func TestExclusive_ReportsAChildThatTheStoreRefusesToQueue(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, err = engine.queueExclusive(engine.ctx, ExclusiveInput{Kind: kindMerge, Project: testProject, Item: "12"})
	require.ErrorContains(t, err, "queue the merge of item 12")
}

func TestExclusive_ReportsAGateWhoseHeadReadTheStoreCannotRecord(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, err = engine.queueExclusive(engine.ctx, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "12"})
	require.ErrorContains(t, err, "record the head of the pull request of item 12")
}

func TestExclusive_ReportsAGateWhoseStopTheStoreCannotRecord(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	err = engine.stopOlderGates(engine.ctx, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "12", Tip: "B"})
	require.ErrorContains(t, err, "record the stop of the old gates of item 12")
}

func TestExclusive_ReportsAQueueThatTheStoreRefuses(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	require.ErrorContains(t, engine.registerExclusiveQueue(), "register the exclusive queue")
}

func TestReopen_ReportsAnExclusiveRunWhoseInputIsNotReadable(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)

	_, err := dbos.Enqueue[any](client.flow, exclusiveQueueName, exclusiveName, "not an exclusive input", dbos.WithEnqueueWorkflowID("exclusive-x"), dbos.WithEnqueueQueuePartitionKey(testProject))
	require.NoError(t, err)

	require.ErrorContains(t, client.rewind("exclusive-x", 0), "read the input of exclusive-x")
}

func TestResume_ACanceledRunWhoseStepsCannotBeReadReportsIt(t *testing.T) {
	t.Parallel()

	client, err := OpenClient(t.Context(), newHarness(t).opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	last := run{Status: dbos.WorkflowStatus{ID: "item-1-r1", Status: dbos.WorkflowStatusCancelled}}

	require.ErrorContains(t, client.reopenRun(last), "read the steps of item-1-r1")
}
