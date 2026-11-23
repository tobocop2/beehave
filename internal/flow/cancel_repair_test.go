package flow

import (
	"database/sql/driver"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"

	"github.com/tobocop2/beehave/internal/merge"
)

// refuseJobCancel makes the store refuse the write that cancels a build job: a fault after the cancel of the levels above the job, as a busy store,
// a full disk or a process killed between two commits leaves it.
const refuseJobCancel = `CREATE TRIGGER refuse_job_cancel BEFORE UPDATE OF status ON workflow_status
WHEN NEW.status = 'CANCELLED' AND OLD.name = 'host_job'
BEGIN SELECT RAISE(ABORT, 'the store refused the write'); END`

const allowJobCancel = "DROP TRIGGER refuse_job_cancel"

// storePause blocks the store function pause_cancel until release.
type storePause struct {
	enter, release func()
	out            chan struct{}
}

// storePauses holds the pause of each test that called newStorePause, by its key, and pauseKeys counts the keys.
var (
	storePauses sync.Map
	pauseKeys   atomic.Int32
)

// errPauseFunction is the result of the registration of pause_cancel. The registration runs when the package loads, before any test opens the store:
// the driver does not guard a registration against a connection that opens at the same time, and only a connection opened after it knows the function.
var errPauseFunction = sqlite.RegisterScalarFunction("pause_cancel", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if found, ok := storePauses.Load(fmt.Sprint(args[0])); ok {
		if pause, ok := found.(*storePause); ok { // the map holds only pauses
			pause.enter()
			hang(pause.out)
		}
	}

	return int64(0), nil
})

// newStorePause returns a trigger that blocks a cancel when it writes a build job, until release is called.
// A cancel then holds the write of the job level open, after the levels above it committed.
func newStorePause(t *testing.T) (trigger string, entered <-chan struct{}, release func()) {
	t.Helper()
	require.NoError(t, errPauseFunction)

	key := "pause-" + strconv.Itoa(int(pauseKeys.Add(1)))
	in := make(chan struct{})
	pause := &storePause{enter: sync.OnceFunc(func() { close(in) }), out: make(chan struct{})}
	pause.release = sync.OnceFunc(func() { close(pause.out) })
	storePauses.Store(key, pause)
	t.Cleanup(pause.release)

	trigger = `CREATE TRIGGER pause_job_cancel BEFORE UPDATE OF status ON workflow_status
WHEN NEW.status = 'CANCELLED' AND OLD.name = 'host_job'
BEGIN SELECT pause_cancel('` + key + `'); END`

	return trigger, in, pause.release
}

// statusIn reads the stored status of one workflow.
func statusIn(t *testing.T, h *harness, id string) dbos.WorkflowStatusType {
	t.Helper()

	var status string

	require.NoError(t, sqlOf(t, h).QueryRowContext(t.Context(), "SELECT status FROM workflow_status WHERE workflow_uuid = ?", id).Scan(&status))

	return dbos.WorkflowStatusType(status)
}

// storeExec runs one statement on the store of the harness.
func storeExec(t *testing.T, h *harness, statement string) {
	t.Helper()

	_, err := sqlOf(t, h).ExecContext(t.Context(), statement)
	require.NoError(t, err)
}

// liveJobUnder adds a running build job under parent and returns its id.
func liveJobUnder(t *testing.T, h *harness, parent string) string {
	t.Helper()

	job := parent + "-live-job"
	_, err := sqlOf(t, h).ExecContext(t.Context(), "INSERT INTO workflow_status (workflow_uuid, status, name, parent_workflow_id) VALUES (?, ?, ?, ?)",
		job, string(dbos.WorkflowStatusPending), hostJobName, parent)
	require.NoError(t, err)

	return job
}

// blockedIn is true when the goroutine with this id runs a frame whose name holds one of the names.
func blockedIn(id string, names ...string) func() bool {
	return func() bool {
		for _, stack := range strings.Split(string(goroutineStacks()), "\n\n") {
			if strings.HasPrefix(stack, "goroutine "+id+" [") {
				return slices.ContainsFunc(names, func(name string) bool { return strings.Contains(stack, name) })
			}
		}

		return false
	}
}

func TestCancel_ACancelThatFailedAtTheBuildJobIsFinishedByTheNextCancel(t *testing.T) {
	t.Parallel()

	entries := map[string]func(c *Client, run string) error{
		"cancel":     func(c *Client, _ string) error { return c.Cancel(testItem) },
		"cancel_run": func(c *Client, run string) error { return c.CancelRun(run) },
	}

	for name, cancel := range entries {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, _, client, run, done := openItemRun(t)
			job := liveJobUnder(t, h, run)
			storeExec(t, h, refuseJobCancel)

			require.ErrorContains(t, cancel(client, run), "the store refused the write")
			require.Equal(t, dbos.WorkflowStatusCancelled, statusIn(t, h, run), "the cancel committed the run")
			require.Equal(t, dbos.WorkflowStatusPending, statusIn(t, h, job), "the cancel stopped before the job")

			storeExec(t, h, allowJobCancel)

			var said strings.Builder

			client.progress = &said
			require.NoError(t, cancel(client, run), "a cancel of the canceled run finishes the cancel")
			assert.Equal(t, dbos.WorkflowStatusCancelled, statusIn(t, h, job), "the second cancel reached the job that the first one left running")
			// The fault aborts the write of the whole level under the run, so the other children of that level stay open too.
			assert.True(t, strings.HasPrefix(said.String(), run+" was already cancelled. This cancel stopped what an earlier cancel left running: "), said.String()) //nolint:misspell // the state word that beehave status prints
			assert.Contains(t, said.String(), job, "the cancel names the job it stopped")

			err := cancel(client, run)
			require.ErrorIs(t, err, ErrWrongState, "a canceled run with nothing open is refused")
			require.ErrorContains(t, err, run+" is cancelled and has nothing to cancel") //nolint:misspell // the state word that beehave status prints
			endsAfterTheCancel(t, h, done)
		})
	}
}

func TestCancel_ReportsATreeThatCannotBeRead(t *testing.T) {
	t.Parallel()

	client, err := OpenClient(t.Context(), newHarness(t).opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, err = client.openUnder("item-12-r1")
	require.ErrorContains(t, err, "list the children of run item-12-r1")
}

func TestSwarm_ARaiseCancelsAgainTheChildrenThatAFailedCancelOfARunLeftRunning(t *testing.T) {
	t.Parallel()

	h, engine, _, run, done := openItemRun(t)
	job := liveJobUnder(t, h, run)
	ref := spawnedRun{Item: testItem, Run: run}
	storeExec(t, h, refuseJobCancel)

	require.Error(t, engine.cancelRun(testProject, ref))
	require.Equal(t, dbos.WorkflowStatusCancelled, statusIn(t, h, run))

	looks := []look{{ref: ref, canceled: true}}

	_, err := engine.cancelOpen(testProject, looks)
	require.ErrorContains(t, err, "cancel the children of run "+run)

	storeExec(t, h, allowJobCancel)

	canceled, err := engine.cancelOpen(testProject, looks)
	require.NoError(t, err)
	assert.Empty(t, canceled, "the raise lists only the open runs it canceled, so a resume keeps a run canceled before")
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusIn(t, h, job), "the raise reached the job that the failed cancel left running")
	endsAfterTheCancel(t, h, done)
}

func TestSuperseded_AGateOfALaterHeadCancelsTheBuildJobThatAFailedCancelOfTheOldGateLeftRunning(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")
	old := queueHostGate(engine, testProject, testItem, "A", "box")

	waitOn(t, startsOfRun(t, engine, gated, 1))

	jobs, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName), dbos.WithFilterStatus(dbos.WorkflowStatusPending))
	require.NoError(t, err)
	require.Len(t, jobs, 1, "the gate of head A runs its build job")

	gate, job := jobs[0].ParentWorkflowID, jobs[0].ID
	stop := func(tip string) []string {
		setHead(h, tip)

		return engine.stopSuperseded(t.Context(), ExclusiveInput{Kind: kindGate, Project: testProject, Item: testItem, Tip: tip})
	}

	storeExec(t, h, refuseJobCancel)
	require.Empty(t, stop("B"), "the cancel of the old gate failed at its job")
	require.Equal(t, dbos.WorkflowStatusCancelled, statusIn(t, h, gate))
	require.Equal(t, dbos.WorkflowStatusPending, statusIn(t, h, job))
	storeExec(t, h, allowJobCancel)

	assert.Equal(t, []string{gate}, stop("C"), "the gate of a later head lists the canceled gate whose job runs")
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusIn(t, h, job), "the cancel reached the job that the failed one left running")
	assert.Empty(t, stop("D"), "a canceled gate whose job ended is not listed again")

	require.ErrorIs(t, endsAfterTicks(t, h, old).err, errSuperseded)
	close(gated.release)
}

// A cancel commits the tree one level at a time. A resume that runs while the cancel holds the write of the job level reads the tree
// only after the cancel ended, so it reopens the job that the cancel reached and leaves no canceled job under the reopened gate.
func TestResume_AResumeDuringACancelReadsTheTreeAfterTheCancelEnds(t *testing.T) {
	t.Parallel()

	entries := map[string]struct {
		version string
		resume  func(c *Client) error
	}{
		"resume":        {testVersion, func(c *Client) error { return c.Resume(testItem) }},
		"on_this_build": {otherBuild, func(c *Client) error { return c.ResumeOnThisBuild(testItem) }},
	}

	for name, entry := range entries {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resumeDuringACancel(t, entry.version, entry.resume)
		})
	}
}

// resumeDuringACancel runs resume on a client of version while a cancel holds the write of the job level, and checks that the reopened gate has a job that is not canceled.
func resumeDuringACancel(t *testing.T, version string, resume func(c *Client) error) {
	t.Helper()

	pause, entered, release := newStorePause(t)
	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.hosts = &upHosts{started: true}
	gated := newHostGate()
	deps := h.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	file := writeFlow(t, bodyFlow("host_up(name = \"start\", host = \"gpu-b\")\ngate(name = \"check\")\n"))
	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- runErr
	}()

	receive(t, gated.started)

	jobs, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName))
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	job := jobs[0].ID
	client := h.client(t, testVersion)

	// A first cancel stops before the job, and the run ends.
	storeExec(t, h, refuseJobCancel)
	require.Error(t, client.Cancel(testItem))
	storeExec(t, h, allowJobCancel)
	h.tick(t)

	require.Error(t, receive(t, ended))

	// A second cancel holds the write of the job level while a resume runs.
	storeExec(t, h, pause)

	canceled := make(chan error, 1)

	go func() { canceled <- client.Cancel(testItem) }()

	select {
	case <-entered:
	case err := <-canceled:
		t.Fatalf("the second cancel ended before it reached the job: %v", err)
	}

	resumer := h.client(t, version)
	ids, resumed := make(chan string, 1), make(chan error, 1)

	go func() {
		ids <- goroutineID()
		resumed <- resume(resumer)
	}()

	// The resume waits: for probe.lock, or for the store after it read the job.
	waitOn(t, condition(blockedIn(receive(t, ids), "flock.(*Flock).Lock", "dbos.RewindWorkflow", "dbos.ResumeWorkflow")))
	release()

	require.NoError(t, receive(t, canceled))
	require.NoError(t, receive(t, resumed))
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, statusIn(t, h, job), "the resume reopened the job that the cancel reached")
}
