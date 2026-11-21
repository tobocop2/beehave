package flow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failFirstThenRelease fails the first command of item 21. The command of item 21 with the count releaseAt releases the commands of item 22, which wait for it.
// So item 22 ends only after the second attempt of 21 runs that far, and a gather with until="any" returns 22 while 21 is open.
func failFirstThenRelease(t *testing.T, h *harness, releaseAt int32) {
	t.Helper()

	var seen21 atomic.Int32

	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })

	h.commands.before = func(_ context.Context, request CommandRequest) error {
		switch request.Item {
		case "21":
			n := seen21.Add(1)
			if n == 1 {
				return errFake
			}

			if n == releaseAt {
				release()
			}
		case "22":
			select {
			case <-released:
			case <-time.After(30 * time.Second):
				t.Error("item 22 waited 30s for the second attempt of item 21")
			}
		}

		return nil
	}
}

// reachFile spawns item 21 with one retry and item 22 with none, and waits until 22 ends. Item 21 fails once, so its second attempt is open at that point.
// The statements of then run in the parent while that second attempt is open.
func reachFile(child, then string) string {
	return `def child(item):
` + child + `

def quick(item):
    command(name = "q", run = ["q"])

def main(item):
    a = spawn(items = ["21"], entry = "child", retries = 1, name = "a")
    b = spawn(items = ["22"], entry = "quick", name = "b")
    ended = gather(a.runs + b.runs, until = "any", name = "first")
` + then + `
    gather(a, name = "all")
`
}

func TestSpawnRetries_ASendToARetriedChildReachesItsSecondAttempt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.MaxConcurrent, h.opts.MaxReviewConcurrent = 3, 3
	failFirstThenRelease(t, h, 2)

	body := reachFile(`    command(name = "c", run = ["c"])
    m = recv("job", timeout = "10s", name = "job")
    command(name = "got", run = ["got", str(m.timed_out), str(m.value)])`,
		`    send(to = a.runs[0], topic = "job", value = "x", name = "give")`)
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, body))

	require.NoError(t, err)
	assert.Equal(t, []string{"c", "got False x"}, h.commands.ranBy("21"), "the second attempt of 21 got the message that the parent sent to the run that spawn returned")
}

func TestSpawnRetries_ProgressOfARetriedChildReadsItsSecondAttempt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.MaxConcurrent, h.opts.MaxReviewConcurrent = 3, 3
	failFirstThenRelease(t, h, 3)

	body := reachFile(`    command(name = "c", run = ["c"])
    publish("stage", "two", name = "stage")
    command(name = "said", run = ["said"])
    recv("done", timeout = "10s", name = "done")`,
		`    p = progress(a, "stage", name = "peek")[0]
    command(name = "log", run = ["log", str(p.set), str(p.value), p.item, p.run])
    send(to = a.runs[0], topic = "done", value = 1, name = "done")`)
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, body))

	require.NoError(t, err)
	assert.Equal(t, []string{"log True two 21 item-21-r2"}, h.commands.ranBy(testItem), "progress reads the second attempt of 21")
}

func TestSpawnRetries_ASendToAChildWithNoOpenAttemptIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	body := `def child(item):
    command(name = "c", run = ["c"])

def main(item):
    a = spawn(items = ["21"], entry = "child", name = "a")
    gather(a, name = "all")
    send(to = a.runs[0], topic = "job", value = "x", name = "late")
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, body))

	require.ErrorContains(t, err, "send: item 21 has no open run: its last attempt item-21-r1 ended")
}

// rowReadFails fails the first list of runs that holds only the run id, after armed is set. The engine reads the rows on a copy that the parent does not cancel,
// so the copy keeps the failure.
type rowReadFails struct {
	dbos.Context

	id    string
	armed *atomic.Bool
}

func (r *rowReadFails) WithoutCancel(ctx dbos.Context) dbos.Context {
	return &rowReadFails{Context: r.Context.WithoutCancel(ctx), id: r.id, armed: r.armed}
}

func (r *rowReadFails) ListWorkflows(_ dbos.Client, opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	rows, err := dbos.ListWorkflows(r.Context, opts...)
	if err == nil && len(rows) == 1 && rows[0].ID == r.id && r.armed.CompareAndSwap(true, false) {
		return nil, errors.New("the store failed")
	}

	return rows, err
}

func TestSpawnRetries_ASendWhoseReadOfTheChildFailsStopsTheStepAndFailsTheParent(t *testing.T) {
	t.Parallel()

	var armed atomic.Bool

	h := newHarness(t)
	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Item == testItem {
			armed.Store(true)
		}

		return nil
	}

	var store dbos.Context

	engine := h.engineWith(t, func(e *Engine) {
		store = e.ctx
		e.ctx = &rowReadFails{Context: e.ctx, id: runID("21", 1), armed: &armed}
	})

	body := `def child(item):
    recv("job", timeout = "3s", name = "job")

def main(item):
    a = spawn(items = ["21"], entry = "child", name = "a")
    command(name = "arm", run = ["arm"])
    send(to = a.runs[0], topic = "job", value = "x", name = "give")
    gather(a, name = "all")
`
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, body))

	require.ErrorContains(t, err, "send: read the runs of the spawned items: the store failed")

	rows, err := dbos.ListWorkflows(store, dbos.WithFilterWorkflowIDs(runID(testItem, 1)))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, dbos.WorkflowStatusError, rows[0].Status, "the parent run ends in error")

	steps, err := dbos.GetWorkflowSteps(store, runID(testItem, 1))
	require.NoError(t, err)

	var sendErr error

	for _, step := range steps {
		if strings.HasPrefix(step.StepName, "send:give") {
			sendErr = step.Error
		}
	}

	require.Error(t, sendErr, "the send step recorded the error")
	assert.Contains(t, sendErr.Error(), "the store failed")
}
