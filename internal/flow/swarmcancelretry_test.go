package flow

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/workflow"
)

// retriedFlow spawns 21 with one retry and 22 with none. The first attempt of 21 fails at its first command. Its second attempt spawns 23, which holds, and
// waits for it. 22 ends once 23 holds, so the first_of returns while the second attempt of 21 and its child are open. The parent then stops
// 21 with the call it is given, and runs a command that names what the call returned.
func retriedFlow(stop string) string {
	return `def inner(item):
    command(name = "hold", run = ["hold"])

def child(item):
    command(name = "c", run = ["c"])
    gather(spawn(items = ["23"], entry = "inner", name = "deep"), name = "deep")

def quick(item):
    command(name = "q", run = ["q"])

def main(item):
    a = spawn(items = ["21"], entry = "child", retries = 1, name = "a")
    b = spawn(items = ["22"], entry = "quick", name = "b")
    first_of(runs = a.runs + b.runs, name = "first")
    got = ` + stop + `
    command(name = "after", run = ["after", got])
`
}

// failOnceThenHold fails the first command of 21, holds the command of 23 until its context ends, and holds the command of 22 until 23 holds.
func failOnceThenHold(h *harness) {
	var seen21 atomic.Int32

	holding := make(chan struct{})
	held := sync.OnceFunc(func() { close(holding) })

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		switch request.Item {
		case "21":
			if seen21.Add(1) == 1 {
				return errFake
			}
		case "22":
			<-holding
		case "23":
			held()
			<-ctx.Done()

			return ctx.Err()
		}

		return nil
	}
}

func TestCancelRetry_EndsTheLatestAttemptOfARetriedChildAndItsChild(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ stop, want string }{
		"cancel":   {`str(cancel(a.runs[0], name = "drop").` + workflow.CancelledField + `)`, "True"},
		"deadline": {`gather(a, timeout = "1s", on_timeout = "cancel", name = "cut").results[0].error`, "deadline"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.opts.MaxConcurrent, h.opts.MaxReviewConcurrent = 4, 4
			failOnceThenHold(h)

			engine := h.engine(t)
			_, err := engine.RunFile(testItem, testProject, writeFlow(t, retriedFlow(c.stop)), modeOff())

			require.NoError(t, err)
			assert.Contains(t, h.commands.ranBy(testItem), "after "+c.want, "the second attempt of 21 was open, so the call ended it")
			assert.Equal(t, dbos.WorkflowStatusError, statusOf(t, engine, "item-21-r1"), "the first attempt stays failed")

			for _, run := range []string{"item-21-r2", "item-23-r1"} {
				waitOn(t, condition(func() bool { return statusOf(t, engine, run) == dbos.WorkflowStatusCancelled }))
			}
		})
	}
}

// TestGatherDeadline_ARetryInsideTheWaitKeepsTheOneDeadline fails the first attempt of 21 after 1.5 s, inside a gather with a deadline of 3 s, and holds the
// second attempt. The deadline step is recorded once, and the wait before the retry and the wait after it each record that same deadline.
func TestGatherDeadline_ARetryInsideTheWaitKeepsTheOneDeadline(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var seen21 atomic.Int32

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Item != "21" {
			return nil
		}

		if seen21.Add(1) == 1 {
			time.Sleep(1500 * time.Millisecond)

			return errFake
		}

		<-ctx.Done()

		return ctx.Err()
	}

	file := `def child(item):
    command(name = "c", run = ["c"])

def main(item):
    a = spawn(items = ["21"], entry = "child", retries = 1, name = "a")
    r = gather(a, timeout = "3s", on_timeout = "cancel", name = "cut").results[0]
    command(name = "after", run = ["after", r.error, str(r.attempts)])
`
	engine := h.engine(t)
	began := time.Now()
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, file), modeOff())
	took := time.Since(began)

	require.NoError(t, err)
	assert.Equal(t, []string{"after deadline 2"}, h.commands.ranBy(testItem), "the second attempt was open at the deadline")

	deadline, waits := recordedDeadlines(t, engine, "gather:cut#1")
	assert.Equal(t, []string{"gather:cut#1", "gather:cut#1.1"}, slices.Sorted(maps.Keys(waits)), "one wait before the retry and one after it")

	for name, end := range waits {
		assert.Equal(t, deadline, end, "the wait %s used the deadline that the call recorded first", name)
	}

	// A loose bound only: a deadline that started again at the retry would take about 4.5 s, and a slow host adds time to both.
	assert.Less(t, took, 20*time.Second)
	waitOn(t, condition(func() bool { return statusOf(t, engine, "item-21-r2") == dbos.WorkflowStatusCancelled }))
}

// recordedDeadlines reads the steps of the parent run: the deadline that the gather step recorded first, and the deadline that each of its wait steps used.
func recordedDeadlines(t *testing.T, engine *Engine, step string) (int64, map[string]int64) {
	t.Helper()

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	var deadline int64

	waits := map[string]int64{}

	for _, one := range steps {
		text, ok := one.Output.(string)
		if !ok || !strings.HasPrefix(one.StepName, step) {
			continue
		}

		switch {
		case one.StepName == step+deadlineSuffix:
			require.NoError(t, json.Unmarshal([]byte(text), &deadline))
		case isGatherStep(one.StepName) && !strings.Contains(one.StepName, ".retry"):
			var done gathered
			require.NoError(t, json.Unmarshal([]byte(text), &done))
			waits[one.StepName] = done.Deadline
		}
	}

	require.NotZero(t, deadline, "the call recorded its deadline")

	return deadline, waits
}
