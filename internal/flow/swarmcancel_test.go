package flow

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

// cancelFlow spawns 21 and 22, cancels the run of 21, gathers both and runs a command that names what cancel returned and how each run ended.
const cancelFlow = `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22"], entry = "worker", name = "fan")
    c = cancel(runs.runs[0], name = "drop")
    done = gather(runs, name = "fan")
    command(name = "after", run = ["after", str(c.cancelled)] + [r.item + ":" + ("ok" if r.ok else "failed") for r in done.results])
`

// deadlineFlow spawns 21 and gathers it under a deadline with the mode, then runs a command that names how the run ended and whether it is open.
func deadlineFlow(timeout, mode string) string {
	return `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21"], entry = "worker", name = "fan")
    command(name = "ready", run = ["ready"])
    r = gather(runs, timeout = "` + timeout + `", on_timeout = "` + mode + `", name = "fan").results[0]
    command(name = "after", run = ["after", str(r.ok), r.error, str(r.open)])
`
}

// statusOf reads the status of a run in the store.
func statusOf(t *testing.T, engine *Engine, run string) dbos.WorkflowStatusType {
	t.Helper()

	rows, err := engine.rowsOf([]spawnedRun{{Run: run}})
	require.NoError(t, err)

	return rows[run].Status
}

func TestCancel_EndsTheOpenChildAndItsSiblingGoesOnToTheEnd(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21")

	defer close(release)

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, cancelFlow), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"after True 21:failed 22:ok"}, h.commands.ranBy(testItem))
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusOf(t, engine, "item-21-r1"), "the store holds the child as canceled")
	assert.Equal(t, dbos.WorkflowStatusSuccess, statusOf(t, engine, "item-22-r1"))
}

func TestCancel_ARunThatSpawnReusedHadEndedSoTheCancelStopsNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	_, err := engine.RunFile("21", testProject, writeFlow(t, bodyFlow(`command(name = "w", run = ["w"])`)), modeOff())
	require.NoError(t, err)

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, cancelFlow), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"after False 21:ok 22:ok"}, h.commands.ranBy(testItem), "the reused run ended before the cancel, so cancel reads False and gather reads it ok")
	assert.Equal(t, dbos.WorkflowStatusSuccess, statusOf(t, engine, "item-21-r1"), "the reused run keeps its success")
	assert.Len(t, h.commands.ranBy("21"), 1, "the reused run did not run again")
}

func TestCancel_ARunThatEndedBeforeTheCallReadsFalse(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21"], entry = "worker", name = "fan")
    gather(runs, name = "fan")
    c = cancel(runs.runs[0], name = "late")
    command(name = "after", run = ["after", str(c.cancelled)])
`
	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"after False"}, h.commands.ranBy(testItem))
	assert.Equal(t, dbos.WorkflowStatusSuccess, statusOf(t, engine, "item-21-r1"))
}

func TestCancel_RefusesARunThatThisRunDidNotSpawnAndAValueThatIsNoRun(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ body, want string }{
		"stranger":   {`cancel(struct(item = "21", run = "item-21-r1"), name = "x")`, "run item-21-r1 is not a child of this run. Pass the runs that spawn returned"},
		"not a run":  {`cancel(7, name = "x")`, "run must be one of the runs that spawn returned, and 7 is not one"},
		"a list":     {`cancel(spawn(["21"]).runs, name = "x")`, "run must be one of the runs that spawn returned"},
		"no keyword": {`cancel()`, "cancel needs the `run` keyword"},
		"no run":     {`cancel(*[])`, "cancel: missing argument for run"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := newHarness(t).engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(c.body)), modeOff())

			require.ErrorContains(t, err, c.want)
		})
	}
}

func TestCancel_AStoreThatCannotBeReadIsReported(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	require.NoError(t, engine.Shutdown())

	ref := spawnedRun{Item: "21", Run: "item-21-r1"}

	_, err := engine.cancelChild(testProject, ref, true)
	require.ErrorContains(t, err, "read the runs of the spawned items")

	_, err = engine.isOpen(ref)
	require.ErrorContains(t, err, "read the runs of the spawned items")

	_, err = engine.cancelRefs(testProject, []spawnedRun{ref})
	require.ErrorContains(t, err, "read the runs of the spawned items")
}

func TestCancel_ACallOutsideAWorkflowRunIsRefusedBeforeItCancels(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	s := &script{e: engine, ctx: engine.ctx}

	_, err := s.cancelOnce("x", spawnedRun{Item: "21", Run: "item-21-r1"})

	require.Error(t, err, "the read of the run is a step, which runs only inside a workflow run")
	assert.Empty(t, s.counts, "a refused read counts no cancel call")
}

// TestCancel_AStepThatRunsAgainAfterACancelStillReadsThatItEndedTheRun runs the cancel step a second time on a run that the first try canceled, as a
// crash between the cancel and the record of the step would. The read of the run before the cancel is a step of its own, so the second try keeps it.
func TestCancel_AStepThatRunsAgainAfterACancelStillReadsThatItEndedTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	startOpenRun(t, h, engine, "21", testProject)

	ref := spawnedRun{Item: "21", Run: "item-21-r1"}
	open, err := engine.isOpen(ref)
	require.NoError(t, err)
	require.True(t, open)

	first, err := engine.cancelChild(testProject, ref, open)
	require.NoError(t, err)

	again, err := engine.cancelChild(testProject, ref, open)
	require.NoError(t, err)

	assert.True(t, first.Ended)
	assert.True(t, again.Ended, "the run was open at the recorded read, so the try after a crash reads that this call ended it")
}

func TestGatherDeadline_CancelEndsTheOpenRunAsDeadlineAndCancelsIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21")

	defer close(release)

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, deadlineFlow("1s", "cancel")), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"ready", "after False deadline False"}, h.commands.ranBy(testItem))
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusOf(t, engine, "item-21-r1"), "the deadline canceled the open run")
}

func TestGatherDeadline_ReturnLeavesTheOpenRunOpenAndReadsOpenTrue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21")

	defer close(release)

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, deadlineFlow("1s", "return")), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"ready", "after False deadline True"}, h.commands.ranBy(testItem))
	assert.Equal(t, dbos.WorkflowStatusPending, statusOf(t, engine, "item-21-r1"), "return leaves the run running")
}

func TestGatherDeadline_ARunThatEndsBeforeTheDeadlineIsItsOwnOutcome(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, deadlineFlow("1h", "cancel")), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"ready", "after True  False"}, h.commands.ranBy(testItem))
}

func TestGatherDeadline_ATimeoutAloneCancelsAtTheDeadline(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21")

	defer close(release)

	file := `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    r = gather(spawn(items = ["21"], entry = "worker", name = "fan"), timeout = "1s", name = "fan").results[0]
    command(name = "after", run = ["after", r.error])
`
	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"after deadline"}, h.commands.ranBy(testItem))
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusOf(t, engine, "item-21-r1"))
}

func TestGatherDeadline_ARefusedModeNamesTheRule(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ body, want string }{
		"mode alone":   {"mode = \"cancel\" + \"\"\ngather([], on_timeout = mode)", "on_timeout needs a timeout"},
		"unknown mode": {"mode = \"wait\" + \"\"\ngather([], timeout = \"1s\", on_timeout = mode)", "on_timeout is \"wait\". Use \"cancel\" or \"return\""},
		"zero":         {"t = \"0s\" + \"\"\ngather([], timeout = t)", "timeout is \"0s\", which must be above zero"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := newHarness(t).engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(c.body)), modeOff())

			require.ErrorContains(t, err, c.want)
		})
	}
}

// deadlineRecorded reads whether the parent recorded the deadline step of its gather.
func deadlineRecorded(engine *Engine) bool {
	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)

	return err == nil && slices.ContainsFunc(steps, func(s dbos.StepInfo) bool { return s.StepName == "gather:fan#1.deadline" })
}

func TestGatherDeadline_ARecoveredGatherKeepsTheDeadlineThatItRecordedFirst(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21")

	defer close(release)

	var ahead atomic.Int64

	began := time.Now()
	h.reading = func() time.Time { return began.Add(time.Since(began) + time.Duration(ahead.Load())) }
	file := writeFlow(t, deadlineFlow("1h", "cancel"))

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	go func() { _, _ = first.RunFile(testItem, testProject, file, modeOff()) }() //nolint:errcheck // the stop of the engine ends the run with an error

	waitOn(t, condition(func() bool { return deadlineRecorded(first) }))
	stop()
	require.NoError(t, first.Shutdown())

	// The clock moves past the recorded deadline. A deadline recorded again at the recovery would lie one hour ahead, and the run would not end.
	ahead.Store(int64(61 * time.Minute))

	_, err = h.engine(t).RunFile(testItem, testProject, file, modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"ready", "after False deadline False"}, h.commands.ranBy(testItem), "the step before the gather ran once, and the first deadline ended the wait")
}

func TestRaised_SkipsTheDeadlineStepAndAStepOfAnotherKind(t *testing.T) {
	t.Parallel()

	steps := []dbos.StepInfo{
		{StepName: "gather:fan#1.deadline", Output: `{"canceled":["item-31-r1"]}`},
		{StepName: "gather:fan#1.cancel", Output: `{"canceled":["item-33-r1"]}`},
		{StepName: "gatherer:x#1", Output: `{"canceled":["item-32-r1"]}`},
		{StepName: "gather:fan#1", Output: `{"canceled":["item-21-r1"]}`},
		{StepName: "gather:fan#1.1", Output: `{"canceled":["item-22-r2"]}`},
	}

	assert.Equal(t, map[string]bool{"item-21-r1": true, "item-22-r2": true}, raised(steps), "the wait after a retry counts, the deadline and its cancel step do not")
}

func TestCancel_AndTheDeadlineReportACancelMarkerThatCannotBeWritten(t *testing.T) {
	t.Parallel()

	for name, file := range map[string]string{"cancel": cancelFlow, "deadline": deadlineFlow("1s", "cancel")} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			release := holdItems(h, "21")

			defer close(release)

			engine := h.engine(t)
			// A file where the folder of the cancel markers belongs makes every marker fail.
			require.NoError(t, os.WriteFile(filepath.Join(engine.dir, cancelsDir), nil, 0o600))

			_, err := engine.RunFile(testItem, testProject, writeFlow(t, file), modeOff())

			require.ErrorContains(t, err, "mark the cancel of run item-21-r1")
			assert.NotContains(t, h.commands.ranBy(testItem), "after", "the call failed before the command after it")
		})
	}
}

func TestGatherDeadline_ADeadlineOutsideAWorkflowRunIsRefused(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	s := &script{e: engine, ctx: engine.ctx}

	timeout := starlark.Tuple{starlark.String(timeoutKeyword), starlark.String("1s")}

	_, err := s.gather(starlark.NewBuiltin(gatherKind, nil), starlark.Tuple{runsValue(nil)}, []starlark.Tuple{timeout})

	require.Error(t, err, "the deadline step runs only inside a workflow run")
	assert.Empty(t, s.counts, "a refused deadline counts no gather call")
}

func TestCancel_ASecondCancelOfTheSameRunReadsFalse(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21")

	defer close(release)

	file := `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21"], entry = "worker", name = "fan")
    first = cancel(runs.runs[0], name = "first")
    again = cancel(runs.runs[0], name = "again")
    command(name = "after", run = ["after", str(first.cancelled), str(again.cancelled)])
`
	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"after True False"}, h.commands.ranBy(testItem), "the run had ended at the second call, so that call ended nothing")
}

// TestGatherDeadline_TheWaitStepRecordsTheDeadlineOutcomesAndCancelsNothing pins the order of a deadline group: the wait step decides each outcome and
// names the runs to cancel, and the step after it cancels them. A crash between a cancel and a record then cannot turn a "deadline" into a canceled run.
func TestGatherDeadline_TheWaitStepRecordsTheDeadlineOutcomesAndCancelsNothing(t *testing.T) {
	t.Parallel()

	looks := []look{
		{ref: spawnedRun{Item: "21", Run: "item-21-r1"}, open: true},
		{ref: spawnedRun{Item: "22", Run: "item-22-r1"}, out: childOutcome{Item: "22", OK: true}},
	}

	cancel, keep := slices.Clone(looks), slices.Clone(looks)
	expired := deadlineOutcomes(cancel, workflow.OnTimeoutCancel)
	kept := deadlineOutcomes(keep, workflow.OnTimeoutReturn)

	assert.Equal(t, []childOutcome{{Item: "21", Error: deadlineError}, {Item: "22", OK: true}}, []childOutcome{cancel[0].out, cancel[1].out})
	assert.Equal(t, []string{"item-21-r1"}, expired, "only the open run is left for the cancel step")
	assert.Equal(t, []childOutcome{{Item: "21", Error: deadlineError, Open: true}, {Item: "22", OK: true}}, []childOutcome{keep[0].out, keep[1].out})
	assert.Empty(t, kept, "return cancels nothing")
}
