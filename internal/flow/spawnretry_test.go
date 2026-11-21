package flow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retryFile is a parent that spawns three children with retries, gathers them, and records the outcome and the attempts of each in a last command.
func retryFile(gatherArgs string) string {
	return `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", retries = 1, name = "fan")
    done = gather(runs, ` + gatherArgs + `name = "fan")
    command(name = "after", run = ["after"] + [r.item + ":" + ("ok" if r.ok else "failed") + ":" + str(r.attempts) for r in done.results])
`
}

// runStatus returns the status of one run by its id.
func runStatus(t *testing.T, engine *Engine, id string) dbos.WorkflowStatusType {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterWorkflowIDs(id))
	require.NoError(t, err)
	require.Len(t, rows, 1, "run %s", id)

	return rows[0].Status
}

// failTimes makes the commands of one item fail for its first n commands. It returns the count of the commands of the item that started.
func failTimes(h *harness, item string, n int32) *atomic.Int32 {
	var seen atomic.Int32

	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Item == item && seen.Add(1) <= n {
			return errFake
		}

		return nil
	}

	return &seen
}

func TestSpawnRetries_AChildThatFailsOnceStartsAgainAsItsNextAttemptAndTheRaiseDoesNotFire(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tries := failTimes(h, "22", 1)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "raise", `)))

	require.NoError(t, err, "a failure with retries left does not raise")
	assert.Equal(t, []string{"after 21:ok:1 22:ok:2 23:ok:1"}, h.commands.ranBy(testItem))
	assert.Equal(t, int32(2), tries.Load(), "the child ran its command in each attempt")

	last, _, err := latestRun(engine.ctx, "22")
	require.NoError(t, err)
	assert.Equal(t, 2, last.Attempt, "the retry is the next attempt of the item")
	assert.Equal(t, testRun, last.Status.ParentWorkflowID, "the retry is a child of the parent")
	assert.Equal(t, []string{"spawn:fan#1", "gather:fan#1", "gather:fan#1.retry1", "gather:fan#1.1", "command:after#1"}, stepNames(t, engine))
}

func TestSpawnRetries_AChildThatFailsEachAttemptIsAFailedOutcomeAfterItsRetries(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tries := failTimes(h, "22", 5)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "return", `)))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 21:ok:1 22:failed:2 23:ok:1"}, h.commands.ranBy(testItem))
	assert.Equal(t, int32(2), tries.Load(), "one retry, then no more")
}

// secondPickFails picks a login for each item, and fails the second pick of its item: the pick of the start again.
type secondPickFails struct {
	noReset

	item  string
	picks *atomic.Int32
}

func (p secondPickFails) Pick(_ context.Context, _, item string, _ []string, _ bool) (string, error) {
	if item == p.item && p.picks.Add(1) == 2 {
		return "", errFake
	}

	return testLogin, nil
}

func TestSpawnRetries_AStartAgainWhoseLoginCannotBePickedFailsTheParent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.picker = secondPickFails{item: "22", picks: &atomic.Int32{}}
	tries := failTimes(h, "22", 5)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "return", `)))

	require.ErrorContains(t, err, errFake.Error())
	assert.Equal(t, int32(1), tries.Load(), "no second attempt started")
	assert.Empty(t, h.commands.ranBy(testItem))
}

func TestSpawnRetries_ThePlanOfAStartAgainReportsAStoppedEngine(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	require.NoError(t, engine.Shutdown())

	_, err := engine.planRestarts(t.Context(), ItemInput{Item: testItem, Project: testProject}, []spawnedRun{{Item: "21", Run: "item-21-r1", Retries: 1}}, []int{0})

	require.Error(t, err, "the plan reads the latest run of the item")
}

func TestSpawnRetries_ARaiseFiresWhenTheRetriesAreSpent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tries := failTimes(h, "22", 5)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "raise", `)))

	require.ErrorContains(t, err, errFake.Error())
	assert.Equal(t, int32(2), tries.Load())
	assert.Empty(t, h.commands.ranBy(testItem), "the raise ends the parent")
}

func TestSpawnRetries_AStartAgainThatTheQueueRefusesFailsTheParent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tries := failTimes(h, "22", 5)
	engine := h.engine(t)

	// A row of another workflow holds the id of the next attempt of item 22, so the library refuses to start the item run under it.
	_, err := dbos.Enqueue[ItemResult](h.client(t, testVersion).flow, nobodyQueue, phaseName, PhaseInput{}, dbos.WithEnqueueWorkflowID(runID("22", 2)), dbos.WithEnqueueApplicationVersion(testVersion))
	require.NoError(t, err)

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "return", `)))

	require.ErrorContains(t, err, "start the run of item 22")
	assert.Equal(t, int32(1), tries.Load(), "the refused start ran no second attempt")
	assert.Empty(t, h.commands.ranBy(testItem))
}

func TestSpawnRetries_ThePlanOfAStartAgainReportsAFailedRunWhoseInputCannotBeRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	enqueueRun(t, h.client(t, testVersion), runID("21", 1), "not an item input", testVersion)

	_, err := engine.planRestarts(t.Context(), ItemInput{Item: testItem, Project: testProject}, []spawnedRun{{Item: "21", Run: runID("21", 1), Retries: 1}}, []int{0})

	require.ErrorContains(t, err, "read the input of run "+runID("21", 1))
}

// crashAtPick picks a login for each item, and crashes the parent at the second pick of its item: the plan of the start again.
type crashAtPick struct {
	noReset

	item  string
	picks *atomic.Int32
	crash *crashOnce
}

func (p crashAtPick) Pick(_ context.Context, _, item string, _ []string, _ bool) (string, error) {
	if item == p.item && p.picks.Add(1) == 2 {
		p.crash.after()
	}

	return testLogin, nil
}

func TestSpawnRetries_ACrashWhileThePlanOfAStartAgainRunsStartsTheNextAttemptOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.picker = crashAtPick{item: "22", picks: &atomic.Int32{}, crash: newCrash(t, h)}

	var (
		tries   atomic.Int32
		running atomic.Pointer[Engine]
	)

	// The first attempt of item 22 fails only when 21 and 23 ended, so the cancel that stands for the crash reaches no open sibling.
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Item != "22" || tries.Add(1) > 1 {
			return nil
		}

		for !itemEnded(running.Load(), "21") || !itemEnded(running.Load(), "23") {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}

		return errFake
	}

	engine := h.engine(t)
	running.Store(engine)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "raise", `)))
	require.Error(t, err, "the crash ends the parent")
	require.Equal(t, []string{"spawn:fan#1", "gather:fan#1"}, stepNames(t, engine), "the crash cut the plan off before its record")

	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))
	require.NoError(t, err)

	last, _, err := latestRun(engine.ctx, "22")
	require.NoError(t, err)
	assert.Equal(t, 2, last.Attempt, "the resume planned and started the second attempt, and no third")
	assert.Equal(t, int32(2), tries.Load(), "the item ran once more after the crash")
	assert.Equal(t, []string{"after 21:ok:1 22:ok:2 23:ok:1"}, h.commands.ranBy(testItem))
}

func TestSpawnRetries_AResumeOfTheParentReopensTheLastAttemptOfARetriedChildOnly(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tries := failTimes(h, "22", 2)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "raise", `)))
	require.ErrorContains(t, err, errFake.Error(), "both attempts of item 22 failed")
	require.Equal(t, []string{"spawn:fan#1", "gather:fan#1", "gather:fan#1.retry1", "gather:fan#1.1"}, stepNames(t, engine))

	// The resume rewinds the parent to the start of the second attempt: the recorded plan of the start again replays, and the start runs again and joins the reopened run.
	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))
	require.NoError(t, err)

	last, _, err := latestRun(engine.ctx, "22")
	require.NoError(t, err)
	assert.Equal(t, 2, last.Attempt, "the resume reopened the second attempt and started no third")
	assert.Equal(t, dbos.WorkflowStatusError, runStatus(t, engine, runID("22", 1)), "the first attempt stays failed")
	assert.Equal(t, int32(3), tries.Load(), "only the reopened attempt ran again")
	assert.Equal(t, []string{"after 21:ok:1 22:ok:2 23:ok:1"}, h.commands.ranBy(testItem))
	h.life.mu.Lock()
	assert.Equal(t, 3, h.life.takes["22"], "one claim for each attempt and one for the reopen")
	h.life.mu.Unlock()
	assert.Equal(t, []string{"spawn:fan#1", "gather:fan#1", "gather:fan#1.retry1", "gather:fan#1.1", "command:after#1"}, stepNames(t, engine))
}

func TestSpawnRetries_OnlyAnItemRunIdNamesAnItem(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, id, item string
		ok             bool
	}{
		{name: "item_run", id: runID("bb-r12", 3), item: "bb-r12", ok: true},
		{name: "phase_of_an_item_run", id: runID("12", 1) + "-5", ok: false},
		{name: "no_child", id: "", ok: false},
		{name: "no_attempt", id: runPrefix + "12", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			item, ok := itemOfRun(tc.id)

			assert.Equal(t, tc.ok, ok)

			if tc.ok {
				assert.Equal(t, tc.item, item)
			}
		})
	}
}

func TestSpawnRetries_ARetriedChildStartsAgainOnTheQueueOfItsDepth(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// Three slots let a retried run that lands on a wrong queue still run, so the test names the queue instead of waiting on a full one.
	h.opts.MaxConcurrent, h.opts.MaxReviewConcurrent = 3, 3

	var seen23, seen24 atomic.Int32

	h.commands.before = func(_ context.Context, request CommandRequest) error {
		switch {
		case request.Item == "23" && seen23.Add(1) == 1:
			return errFake
		case request.Item == "24" && seen24.Add(1) == 1:
			return errFake
		}

		return nil
	}

	engine := h.engine(t)
	body := `def l2(item):
    gather(spawn(items = ["23"], entry = "l3", retries = 1, name = "s3"), name = "g3")

def l3(item):
    command(name = "at", run = ["at"])
    gather(spawn(items = ["24"], entry = "l4", retries = 1, name = "s4"), name = "g4")

def l4(item):
    command(name = "leaf", run = ["leaf"])

def main(item):
    r = gather(spawn(items = ["21"], entry = "l2", name = "s2"), name = "g2").results[0]
    if not r.ok:
        fail(r.error)
`
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, body))
	require.NoError(t, err)

	for _, tc := range []struct {
		item  string
		depth int
	}{{"23", 2}, {"24", 3}} {
		in, status := inputOf(t, engine, tc.item)
		last, _, err := latestRun(engine.ctx, tc.item)
		require.NoError(t, err)
		assert.Equal(t, 2, last.Attempt, "item %s ran a second attempt", tc.item)
		assert.Equal(t, tc.depth, in.Depth, "item %s keeps its depth", tc.item)
		assert.Equal(t, itemsQueueOf(tc.depth), status.QueueName, "the second attempt of item %s goes on the queue of its depth", tc.item)
	}
}
