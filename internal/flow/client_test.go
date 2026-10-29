package flow

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/workflow"
)

func TestStateOf_MapsEveryStoreStatusToARunState(t *testing.T) {
	t.Parallel()

	tests := map[dbos.WorkflowStatusType]runState{
		dbos.WorkflowStatusPending:                     runRunning,
		dbos.WorkflowStatusEnqueued:                    runQueued,
		dbos.WorkflowStatusDelayed:                     runQueued,
		dbos.WorkflowStatusSuccess:                     runDone,
		dbos.WorkflowStatusError:                       runFailed,
		dbos.WorkflowStatusMaxRecoveryAttemptsExceeded: runFailed,
		dbos.WorkflowStatusCancelled:                   runCancelled,
		"SOMETHING_NEW":                                runRunning,
	}

	for status, want := range tests {
		assert.Equal(t, want, stateOf(status), status)
	}
}

func TestClient_CommandsRefuseWhatDoesNotFit(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	require.ErrorIs(t, client.Answer("not an id", "x"), ErrBadItem)
	require.ErrorContains(t, client.Answer("not an id", "x"), "use an item id such as 12 or fix-login")
	require.ErrorIs(t, client.Cancel("not an id"), ErrBadItem)
	require.ErrorIs(t, client.Resume("not an id"), ErrBadItem)
	require.ErrorIs(t, client.Answer(testItem, ""), errEmptyAnswer)
	require.ErrorIs(t, client.Answer(testItem, "x"), errNoRun)
	require.ErrorContains(t, client.Cancel(testItem), "start item 12 with obee run 12")

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	require.ErrorIs(t, client.Answer(testItem, "x"), errWrongState)
	require.ErrorIs(t, client.Cancel(testItem), errWrongState)
	require.ErrorIs(t, client.Resume(testItem), errWrongState)
	require.ErrorContains(t, client.Resume(testItem), "only a cancelled or failed run resumes") //nolint:misspell // user-facing state word
}

func TestClient_StatusListsARunThatAsksNoQuestion(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	release := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(release)

	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })

	infos, err := client.Status()
	require.NoError(t, err, "a run in its agent step has no question and is not an error")
	require.Len(t, infos, 1)
	assert.Equal(t, runRunning, infos[0].State)
	assert.Empty(t, infos[0].Question)

	release()
	require.NoError(t, (<-done).err)
}

func TestRunCancelled_SpellsTheWordOfTheAgentOutcome(t *testing.T) {
	t.Parallel()

	assert.Equal(t, string(agent.OutcomeCancelled), string(runCancelled))
}

func TestClient_CancelStopsTheRunAndResumeReopensIt(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)

	infos, err := client.Status()
	require.NoError(t, err)
	assert.Equal(t, "What now?", infos[0].Question, "status shows the question of a paused run")

	require.NoError(t, client.Cancel(testItem))

	infos, err = client.Status()
	require.NoError(t, err)
	assert.Equal(t, runCancelled, infos[0].State)

	other := harness.client(t, "v2")
	require.ErrorIs(t, other.Resume(testItem), errStranded)

	// The tick of the running copy sees the cancel and wakes the owner wait.
	harness.tick(t)

	select {
	case outcome := <-done:
		require.Error(t, outcome.err)
	case <-time.After(eventTimeout):
		t.Fatal("the run kept waiting for the owner after another process canceled it")
	}

	require.NoError(t, client.Resume(testItem))
	require.NoError(t, client.Answer(testItem, "go on"))
	eventually(t, func() bool {
		rows, statusErr := client.Status()

		return statusErr == nil && rows[0].State == runDone
	})
}

func TestLatestRun_SkipsRunsWhoseNameEndsInNoAttemptNumber(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)

	file := filepath.Join(filepath.Dir(harness.opts.StateDir), defaultFlowName)
	pinned, err := workflow.Pin(file)
	require.NoError(t, err)

	handle, err := dbos.RunWorkflow(engine.ctx, engine.item, ItemInput{Item: testItem, File: file, Files: pinned, Login: "alice"}, dbos.WithWorkflowID("item-12-rx"))
	require.NoError(t, err)
	_, err = handle.GetResult()
	require.NoError(t, err)

	_, found, err := latestRun(engine.ctx, testItem)

	require.NoError(t, err)
	assert.False(t, found)
}

func TestClient_StatusReportsAStoreThatIsGone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, err = client.Status()
	require.ErrorContains(t, err, "list the runs")

	_, _, err = latestRun(client.flow, testItem)
	require.ErrorContains(t, err, "list the runs of item 12")
}
