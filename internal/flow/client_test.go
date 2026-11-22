package flow

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/workflow"
)

func TestStateOf_MapsEveryStoreStatusToARunState(t *testing.T) {
	t.Parallel()

	tests := map[dbos.WorkflowStatusType]RunState{
		dbos.WorkflowStatusPending:                     runRunning,
		dbos.WorkflowStatusEnqueued:                    runQueued,
		dbos.WorkflowStatusDelayed:                     runQueued,
		dbos.WorkflowStatusSuspended:                   runQueued,
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

func TestRun_OnlyAnEndedRunIsTerminal(t *testing.T) {
	t.Parallel()

	tests := map[dbos.WorkflowStatusType]bool{
		dbos.WorkflowStatusPending:                     false,
		dbos.WorkflowStatusEnqueued:                    false,
		dbos.WorkflowStatusDelayed:                     false,
		dbos.WorkflowStatusSuspended:                   false,
		dbos.WorkflowStatusSuccess:                     true,
		dbos.WorkflowStatusError:                       true,
		dbos.WorkflowStatusMaxRecoveryAttemptsExceeded: true,
		dbos.WorkflowStatusCancelled:                   true,
	}

	for status, want := range tests {
		assert.Equal(t, want, run{Status: dbos.WorkflowStatus{Status: status}}.terminal(), status)
	}
}

func TestClient_CommandsRefuseWhatDoesNotFit(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	require.ErrorIs(t, client.Reply("not an id", Reply{Text: "x"}), ErrBadItem)
	require.ErrorContains(t, client.Reply("not an id", Reply{Text: "x"}), "use an item id such as 12 or fix-login")
	require.ErrorIs(t, client.Cancel("not an id"), ErrBadItem)
	require.ErrorIs(t, client.Resume("not an id"), ErrBadItem)
	require.ErrorIs(t, client.Reply(testItem, Reply{Text: ""}), errEmptyAnswer)
	require.ErrorIs(t, client.Reply(testItem, Reply{Text: "x"}), errNoRun)
	require.ErrorContains(t, client.Cancel(testItem), "start item 12 with beehave run 12")

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	require.ErrorIs(t, client.Reply(testItem, Reply{Text: "x"}), errWrongState)
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
	waitOn(t, waiting[struct{}]{what: "the agent to start", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, harness.agents.running() == 1, nil }})

	infos, err := client.Status()
	require.NoError(t, err, "a run in its agent step has no question and is not an error")
	require.Len(t, infos, 1)
	assert.Equal(t, runRunning, infos[0].State)
	assert.Empty(t, infos[0].Question)

	release()
	require.NoError(t, receive(t, done).err)
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
	waitOn(t, ownerAsks(t, harness))

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

	require.Error(t, receive(t, done).err, "the run ends after another process canceled it")

	require.NoError(t, client.Resume(testItem))
	require.NoError(t, client.Reply(testItem, Reply{Text: "go on"}))
	waitOn(t, waiting[struct{}]{what: "the resumed run to end done", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		rows, statusErr := client.Status()

		return struct{}{}, statusErr == nil && rows[0].State == runDone, nil
	}})
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
