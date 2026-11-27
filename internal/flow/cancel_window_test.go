package flow

import (
	"bytes"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errNoOldGate = errors.New("the stop of the old gates canceled no gate")

// goroutineID reads the id of the calling goroutine from the first line of its stack.
func goroutineID() string {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	fields := bytes.Fields(buf)

	return string(fields[1])
}

// waitsForTheStoreLock is true when the goroutine with this id runs the DBOS cancel and waits to begin a write transaction.
func waitsForTheStoreLock(id string) func() bool {
	return func() bool {
		for _, stack := range strings.Split(string(goroutineStacks()), "\n\n") {
			if strings.HasPrefix(stack, "goroutine "+id+" [") {
				return strings.Contains(stack, "sysdb.(*SysDB).CancelWorkflows") && strings.Contains(stack, "database/sql.(*DB).BeginTx")
			}
		}

		return false
	}
}

// startJobDuringCancel starts a build job under parent while cancel runs, and returns the status of the job after the cancel.
// The job row commits while the cancel waits for the write lock of the store, so the row is not visible to any read that the cancel made before its first write.
func startJobDuringCancel(t *testing.T, h *harness, parent string, cancel func() error) dbos.WorkflowStatusType {
	t.Helper()

	ctx := t.Context()
	conn, err := sqlOf(t, h).Conn(ctx)
	require.NoError(t, err)

	defer func() { require.NoError(t, conn.Close()) }()

	_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	require.NoError(t, err)

	job := parent + "-job-in-the-cancel"
	_, err = conn.ExecContext(ctx, "INSERT INTO workflow_status (workflow_uuid, status, name, parent_workflow_id) VALUES (?, ?, ?, ?)",
		job, string(dbos.WorkflowStatusPending), hostJobName, parent)
	require.NoError(t, err)

	ids := make(chan string, 1)
	canceled := make(chan error, 1)

	go func() {
		ids <- goroutineID()
		canceled <- cancel()
	}()

	waitOn(t, condition(waitsForTheStoreLock(receive(t, ids))))

	_, err = conn.ExecContext(ctx, "COMMIT")
	require.NoError(t, err)

	require.NoError(t, receive(t, canceled))

	var status string

	require.NoError(t, conn.QueryRowContext(ctx, "SELECT status FROM workflow_status WHERE workflow_uuid = ?", job).Scan(&status))

	return dbos.WorkflowStatusType(status)
}

// openItemRun starts the test item on an engine whose agent never ends, and returns the engine, the client and the id of the open run.
func openItemRun(t *testing.T) (*harness, *Engine, *Client, string, <-chan runOutcome) {
	t.Helper()

	h := newHarness(t)
	h.agents.gate = make(chan struct{})
	engine := h.engine(t)
	client := h.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, waiting[struct{}]{what: "the agent of the open run to start", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, h.agents.running() == 1, nil }})

	return h, engine, client, mustNewest(t, client).Status.ID, done
}

// endsAfterTheCancel ticks the engine once and waits for the canceled run to end.
func endsAfterTheCancel(t *testing.T, h *harness, done <-chan runOutcome) {
	t.Helper()

	h.tick(t)

	require.Error(t, receive(t, done).err)
}

func TestCancel_ABuildJobThatStartsDuringTheCancelOfItsRunIsCanceledWithIt(t *testing.T) {
	t.Parallel()

	h, _, client, run, done := openItemRun(t)

	status := startJobDuringCancel(t, h, run, func() error { return client.Cancel(testItem) })

	assert.Equal(t, dbos.WorkflowStatusCancelled, status, "beehest cancel reached the job that started while it listed the children of the run")
	endsAfterTheCancel(t, h, done)
}

// The swarm cancels each open run with this call when a run of the swarm fails under fail="raise".
func TestSwarm_ABuildJobThatStartsDuringTheCancelOfAnOpenRunIsCanceledWithIt(t *testing.T) {
	t.Parallel()

	h, engine, _, run, done := openItemRun(t)

	status := startJobDuringCancel(t, h, run, func() error { return engine.cancelRun(testProject, spawnedRun{Item: testItem, Run: run}) })

	assert.Equal(t, dbos.WorkflowStatusCancelled, status, "the cancel of a swarm reached the job that started while it listed the children of the run")
	endsAfterTheCancel(t, h, done)
}

func TestSuperseded_ABuildJobThatStartsDuringTheCancelOfTheOldGateIsCanceledWithIt(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	old := queueHostGate(engine, testProject, testItem, "A", "box")

	waitOn(t, startsOfRun(t, engine, gated, 1))
	setHead(h, "B")

	gates, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName), dbos.WithFilterStatus(dbos.WorkflowStatusPending))
	require.NoError(t, err)
	require.Len(t, gates, 1, "only the gate of the old head runs")

	stop := func() error {
		if stopped := engine.stopSuperseded(t.Context(), ExclusiveInput{Kind: kindGate, Project: testProject, Item: testItem, Tip: "B"}); len(stopped) != 1 {
			return errNoOldGate
		}

		return nil
	}

	status := startJobDuringCancel(t, h, gates[0].ID, stop)

	assert.Equal(t, dbos.WorkflowStatusCancelled, status, "the cancel of the old gate reached the job that started while it listed the children of the gate")
	require.ErrorIs(t, endsAfterTicks(t, h, old).err, errSuperseded)
	close(gated.release)
}

func TestGoroutineID_IsTheNumberOnTheFirstLineOfTheStack(t *testing.T) {
	t.Parallel()

	id := goroutineID()

	_, err := strconv.Atoi(id)
	require.NoError(t, err)
	assert.False(t, waitsForTheStoreLock(id)(), "this goroutine runs no cancel")
}
