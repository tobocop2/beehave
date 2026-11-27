package flow

import (
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refuseRunCancel makes the store refuse the write that cancels the run of testItem, as a busy store or a full disk does.
const refuseRunCancel = `CREATE TRIGGER refuse_run_cancel BEFORE UPDATE OF status ON workflow_status
WHEN NEW.status = 'CANCELLED' AND OLD.workflow_uuid = 'item-12-r1'
BEGIN SELECT RAISE(ABORT, 'the store refused the write'); END`

// logRunStatus records each status change of the run of testItem in the table status_log.
const logRunStatus = `CREATE TABLE status_log (seq INTEGER PRIMARY KEY AUTOINCREMENT, old TEXT, new TEXT);
CREATE TRIGGER log_run_status AFTER UPDATE ON workflow_status
WHEN OLD.workflow_uuid = 'item-12-r1'
BEGIN INSERT INTO status_log (old, new) VALUES (OLD.status, NEW.status); END`

// failedOnOtherBuild leaves a failed run of the build testVersion and returns a client of another build, after the workflow file changed.
func failedOnOtherBuild(t *testing.T) (*harness, *Engine, *Client) {
	t.Helper()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "fake failure")

	harness.flowIs(t, defaultFlow+"\n# edited\n")

	return harness, engine, harness.client(t, otherBuild)
}

// storedRun reads one run with its recorded error.
func storedRun(t *testing.T, c *Client, id string) dbos.WorkflowStatus {
	t.Helper()

	rows, err := dbos.ListWorkflows(c.flow, dbos.WithFilterWorkflowIDs(id), dbos.WithFilterLoadOutput(true))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	return rows[0]
}

// requireFailedWithItsError asserts that the run is still failed, waits on no queue and keeps the error of the failing step.
func requireFailedWithItsError(t *testing.T, c *Client, id string) {
	t.Helper()

	row := storedRun(t, c, id)
	require.Equal(t, dbos.WorkflowStatusError, row.Status)
	require.Empty(t, row.QueueName, "the run waits on no queue")
	require.ErrorContains(t, row.Error, "fake failure")
}

func TestCancel_AFailedRunWhoseWorkflowChangedIsCancelledFromTheNewBuild(t *testing.T) {
	t.Parallel()

	_, _, other := failedOnOtherBuild(t)

	err := other.ResumeOnThisBuild(testItem)
	require.ErrorContains(t, err, "changed since the run started", "the move to this build refuses the changed file")

	require.NoError(t, other.Cancel(testItem))
	assert.Equal(t, runCancelled, stateOfRun(t, other, testRun))

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "the cancel does not move the run to this build")
}

func TestCancelRun_AFailedRunIsCancelledByItsRunIdFromAnotherBuild(t *testing.T) {
	t.Parallel()

	_, _, other := failedOnOtherBuild(t)

	require.NoError(t, other.CancelRun(testRun))
	assert.Equal(t, runCancelled, stateOfRun(t, other, testRun))
}

func TestCancel_ACancelledFailedRunLetsTheItemStartAgain(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "fake failure")

	require.NoError(t, client.Cancel(testItem))

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	_, err = engine.Run(testItem, testProject)
	require.NoError(t, err, "a canceled run no longer blocks a new attempt")
	assert.Equal(t, runDone, stateOfRun(t, client, "item-12-r2"))
}

func TestCancel_ADoneRunAndACancelledRunWithNothingOpenAreStillRefused(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	err = client.Cancel(testItem)
	require.ErrorIs(t, err, ErrWrongState)
	require.ErrorContains(t, err, testRun+" is done and has nothing to cancel")

	failed := newHarness(t)
	failed.agents.err = errFake
	failedClient := failed.client(t, testVersion)

	_, err = failed.engine(t).Run(testItem, testProject)
	require.ErrorContains(t, err, "fake failure")
	require.NoError(t, failedClient.Cancel(testItem))

	err = failedClient.Cancel(testItem)
	require.ErrorIs(t, err, ErrWrongState)
	require.ErrorContains(t, err, testRun+" is cancelled and has nothing to cancel") //nolint:misspell // the state word that beehest status prints
}

func TestCancel_ACancelOfAFailedRunKeepsTheStepsItRecorded(t *testing.T) {
	t.Parallel()

	_, engine, other := failedOnOtherBuild(t)

	before, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)
	require.NotEmpty(t, before, "the failed run recorded steps")

	require.NoError(t, other.Cancel(testItem))
	require.Equal(t, runCancelled, stateOfRun(t, other, testRun), "the cancel ended the run")

	after, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)
	assert.Len(t, after, len(before), "the cancel dropped no recorded step, so no replay can start from an earlier one")
}

func TestCancel_ARunOutOfRecoveryAttemptsIsCancelled(t *testing.T) {
	t.Parallel()

	harness, _, other := failedOnOtherBuild(t)
	storeExec(t, harness, `UPDATE workflow_status SET status = '`+string(dbos.WorkflowStatusMaxRecoveryAttemptsExceeded)+`' WHERE workflow_uuid = '`+testRun+`'`)
	require.Equal(t, runFailed, stateOfRun(t, other, testRun), "beehest status prints a run out of recovery attempts as failed")

	require.NoError(t, other.Cancel(testItem))
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusIn(t, harness, testRun))
	require.ErrorContains(t, storedRun(t, other, testRun).Error, "fake failure", "the cancel keeps the recorded error")
}

func TestCancel_ACancelOfAFailedRunKeepsTheErrorItRecorded(t *testing.T) {
	t.Parallel()

	_, _, other := failedOnOtherBuild(t)
	requireFailedWithItsError(t, other, testRun)

	require.NoError(t, other.Cancel(testItem))

	row := storedRun(t, other, testRun)
	assert.Equal(t, dbos.WorkflowStatusCancelled, row.Status)
	require.ErrorContains(t, row.Error, "fake failure", "the cancel keeps the error of the failing step")
}

func TestCancel_AFailedRunIsNeverQueuedByItsCancel(t *testing.T) {
	t.Parallel()

	harness, _, other := failedOnOtherBuild(t) // the engine of the run's build stays live during the cancel
	storeExec(t, harness, logRunStatus)

	require.NoError(t, other.Cancel(testItem))

	rows, err := sqlOf(t, harness).QueryContext(t.Context(), "SELECT old, new FROM status_log ORDER BY seq")
	require.NoError(t, err)

	defer func() { require.NoError(t, rows.Close()) }()

	var changes []string

	for rows.Next() {
		var before, after string
		require.NoError(t, rows.Scan(&before, &after))
		changes = append(changes, before+" -> "+after)
	}

	require.NoError(t, rows.Err())
	assert.Equal(t, []string{string(dbos.WorkflowStatusError) + " -> " + string(dbos.WorkflowStatusCancelled)}, changes, "the run goes from failed to canceled in one write")
}

func TestCancel_ACancelTheStoreRefusesLeavesTheFailedRunFailed(t *testing.T) {
	t.Parallel()

	entries := map[string]func(c *Client) error{
		"cancel":     func(c *Client) error { return c.Cancel(testItem) },
		"cancel_run": func(c *Client) error { return c.CancelRun(testRun) },
	}

	for name, cancel := range entries {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness, _, other := failedOnOtherBuild(t)
			storeExec(t, harness, refuseRunCancel)

			require.ErrorContains(t, cancel(other), "the store refused the write")
			requireFailedWithItsError(t, other, testRun)

			storeExec(t, harness, "DROP TRIGGER refuse_run_cancel")

			require.NoError(t, cancel(other), "a second cancel ends the run")
			assert.Equal(t, runCancelled, stateOfRun(t, other, testRun))
		})
	}
}

func TestCancel_AFailedRunWhoseCancelWasRefusedStaysFailedAcrossAnEngineStartOnTheNewBuild(t *testing.T) {
	t.Parallel()

	harness, engine, other := failedOnOtherBuild(t)
	storeExec(t, harness, refuseRunCancel)
	require.ErrorContains(t, other.Cancel(testItem), "the store refused the write")
	storeExec(t, harness, "DROP TRIGGER refuse_run_cancel")

	require.NoError(t, engine.Shutdown())

	harness.opts.Version = otherBuild
	harness.engine(t) // the start sweeps the cancel markers and recovers the runs of its build

	requireFailedWithItsError(t, other, testRun)
	require.NoError(t, other.Cancel(testItem), "the second cancel ends the run after the restart")
	assert.Equal(t, runCancelled, stateOfRun(t, other, testRun))
}

func TestCancel_AFailedRunWithAnOpenChildCancelsTheChildAndLeavesAFailedChildFailed(t *testing.T) {
	t.Parallel()

	harness, _, other := failedOnOtherBuild(t)
	open := liveJobUnder(t, harness, testRun)
	ended := testRun + "-failed-job"

	_, err := sqlOf(t, harness).ExecContext(t.Context(), "INSERT INTO workflow_status (workflow_uuid, status, name, parent_workflow_id) VALUES (?, ?, ?, ?)",
		ended, string(dbos.WorkflowStatusError), hostJobName, testRun)
	require.NoError(t, err)

	require.NoError(t, other.Cancel(testItem))

	assert.Equal(t, dbos.WorkflowStatusCancelled, statusIn(t, harness, testRun))
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusIn(t, harness, open), "the open child stops with its parent")
	assert.Equal(t, dbos.WorkflowStatusError, statusIn(t, harness, ended), "a child that failed stays failed")
}

func TestCancel_AFailedRootCancelledBeforeItsChildIsFinishedByTheNextCancel(t *testing.T) {
	t.Parallel()

	harness, _, other := failedOnOtherBuild(t)
	job := liveJobUnder(t, harness, testRun)
	storeExec(t, harness, refuseJobCancel)

	require.ErrorContains(t, other.Cancel(testItem), "the store refused the write")
	require.Equal(t, dbos.WorkflowStatusCancelled, statusIn(t, harness, testRun), "the cancel committed the failed root")
	require.Equal(t, dbos.WorkflowStatusPending, statusIn(t, harness, job), "the cancel stopped before the child")

	storeExec(t, harness, allowJobCancel)

	var said strings.Builder

	other.progress = &said
	require.NoError(t, other.Cancel(testItem), "a cancel of the canceled root finishes the cancel")
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusIn(t, harness, job))
	assert.Contains(t, said.String(), "This cancel stopped what an earlier cancel left running: "+job)
}
