package flow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/review"
)

// supersedeGate holds each gate until the test lets it end or its context ends, and counts the gates that started and the ones that its context stopped.
type supersedeGate struct {
	started *atomic.Int32
	stopped *atomic.Int32
	release chan struct{}
	tip     string
}

func (g supersedeGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{Head: g.tip}, nil
}

func (supersedeGate) Submit(context.Context, string, string, string, string, string, string) (GateJob, error) {
	return GateJob{}, nil
}

func (g supersedeGate) Run(ctx context.Context, _, _, _ string, _ GateJob) (GateResult, error) {
	g.started.Add(1)

	select {
	case <-g.release:
		return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
	case <-ctx.Done():
		g.stopped.Add(1)

		return GateResult{}, ctx.Err()
	}
}

func newSupersedeGate(tip string) supersedeGate {
	return supersedeGate{started: &atomic.Int32{}, stopped: &atomic.Int32{}, release: make(chan struct{}), tip: tip}
}

type gateEnd struct {
	out ExclusiveOutput
	err error
}

// gateEnds holds the channel of each test run that queues a gate, by the id of the run.
var (
	gateEnds   = map[string]chan gateEnd{}
	gateEndsMu sync.Mutex
	gateRuns   atomic.Int64
)

// gateQueuer is the run that queues one gate, as a run of a flow does: the head read and the cancel are recorded steps of the run that queues.
type gateQueuer struct{ engine *Engine }

func (q gateQueuer) queue(ctx dbos.Context, in ExclusiveInput) (string, error) {
	id, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return "", err //nolint:wrapcheck // a test helper
	}

	gateEndsMu.Lock()
	end := gateEnds[id]
	gateEndsMu.Unlock()

	out, err := q.engine.queueExclusive(ctx, in)
	end <- gateEnd{out: out, err: err}

	return "", nil
}

// queueGate queues the gate of head of the test item on the exclusive queue and returns the channel of its end.
func queueGate(engine *Engine, head string) <-chan gateEnd {
	return queueGateOf(engine, testProject, testItem, head)
}

// queueGateOf queues the gate of an item of a project from a run, and returns the channel of its end.
func queueGateOf(engine *Engine, project, item, head string) <-chan gateEnd {
	_, ended := queueGateRunOf(engine, project, item, head)

	return ended
}

// queueGateRun queues the gate of head of the test item, and returns the id of the run that queues it with the channel of its end.
func queueGateRun(engine *Engine, head string) (string, <-chan gateEnd) {
	return queueGateRunOf(engine, testProject, testItem, head)
}

// stopRecorded is true when the run that queued a gate has recorded its stop of the old gates. A gate that queues before that is an old gate to it.
func stopRecorded(engine *Engine, run string) func() bool {
	return func() bool {
		steps, err := dbos.GetWorkflowSteps(engine.ctx, run)

		return err == nil && slices.ContainsFunc(steps, func(s dbos.StepInfo) bool { return s.StepName == "gate:stop" })
	}
}

func queueGateRunOf(engine *Engine, project, item, head string) (string, <-chan gateEnd) {
	ended := make(chan gateEnd, 1)
	id := fmt.Sprintf("queue-gate-%d", gateRuns.Add(1))
	gateEndsMu.Lock()
	gateEnds[id] = ended
	gateEndsMu.Unlock()

	_, err := dbos.RunWorkflow(engine.ctx, gateQueuer{engine}.queue, ExclusiveInput{Kind: kindGate, Project: project, Item: item, Head: head, Request: CommandRequest{Project: project, Item: item}}, dbos.WithWorkflowID(id))
	if err != nil {
		ended <- gateEnd{err: err}
	}

	return id, ended
}

func supersedeEngine(t *testing.T, gated supersedeGate, head string) (*harness, *Engine) {
	t.Helper()

	return supersedeEngineWith(t, gated, head, nil)
}

// supersedeEngineWith builds the engine of a supersede test. A non-nil wrap puts a forge around the fake one.
func supersedeEngineWith(t *testing.T, gated supersedeGate, head string, wrap func(*fakeForge) forge) (*harness, *Engine) {
	t.Helper()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: head}
	deps := h.deps()
	deps.Gate = gated

	if wrap != nil {
		deps.Forge = wrap(h.forge)
	}

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	dbos.RegisterWorkflow(engine.ctx, gateQueuer{engine}.queue, dbos.WithWorkflowName("test-queue-gate"))
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return h, engine
}

const (
	// logLinesInDump is the number of warnings and errors of the engine log that a dump keeps.
	logLinesInDump = 20
	// dumpBound is the time that a dump may take. A busy store can block the reads of the dump, and the message of the failed wait must still appear.
	dumpBound = 10 * time.Second
)

// errMoreGatesThanExpected is the failure of a wait for a count of gates when the count went past the count that the test expects.
var errMoreGatesThanExpected = errors.New("more gates than expected")

// within returns the text that build makes, or a line that says build did not finish in bound.
func within(bound time.Duration, build func() string) string {
	done := make(chan string, 1)

	go func() { done <- build() }()

	timer := time.NewTimer(bound)
	defer timer.Stop()

	select {
	case text := <-done:
		return text
	case <-timer.C:
		return fmt.Sprintf("the dump did not finish in %s\n", bound)
	}
}

// gateDump is the state that a failed wait prints: the counts of the gate, each workflow of the engine with its status, queue and partition,
// the steps recorded for each workflow, and the last warnings and errors of the engine log. It reads the store of the engine, as the engine does.
func gateDump(engine *Engine, g supersedeGate) string {
	return gateDumpWithin(dumpBound, engine, g)
}

// gateDumpWithin is gateDump with the time that the reads may take.
func gateDumpWithin(bound time.Duration, engine *Engine, g supersedeGate) string {
	return within(bound, func() string { return gateState(engine, g) })
}

func gateState(engine *Engine, g supersedeGate) string {
	return fmt.Sprintf("gates started %d, stopped %d\n%s", g.started.Load(), g.stopped.Load(), workflowState(engine))
}

// The texts of the two lines that the engine of DBOS logs at Info when a run cancels itself through its own context.
const (
	cancelStartedMarker = "Cancelling workflow"    //nolint:misspell // the spelling of the DBOS log line
	cancelEndedMarker   = "Workflow was cancelled" //nolint:misspell // the spelling of the DBOS log line
)

// dumpTime is the time in a form that sorts as text, or a dash for a time that is not set.
func dumpTime(at time.Time) string {
	if at.IsZero() {
		return "-"
	}

	return at.UTC().Format("15:04:05.000000")
}

// workflowState is the part of the dump that needs no gate: each workflow of the engine with its status, times, executor, queue, partition and parent,
// the steps of each with their times, every cancel line of the engine log, and the last warnings and errors of the engine log.
func workflowState(engine *Engine) string {
	var out strings.Builder

	rows, err := dbos.ListWorkflows(engine.ctx)
	if err != nil {
		fmt.Fprintf(&out, "cannot list the workflows: %v\n", err)
	}

	for _, row := range rows {
		fmt.Fprintf(&out, "workflow %s name=%s status=%s queue=%q partition=%q updated_at=%s completed_at=%s executor=%q parent=%q\n",
			row.ID, row.Name, row.Status, row.QueueName, row.QueuePartitionKey, dumpTime(row.UpdatedAt), dumpTime(row.CompletedAt), row.ExecutorID, row.ParentWorkflowID)

		steps, err := dbos.GetWorkflowSteps(engine.ctx, row.ID)
		if err != nil {
			fmt.Fprintf(&out, "  cannot read the steps: %v\n", err)
		}

		for _, step := range steps {
			fmt.Fprintf(&out, "  step %d %s started_at=%s completed_at=%s error=%v\n", step.StepID, step.StepName, dumpTime(step.StartedAt), dumpTime(step.CompletedAt), step.Error)
		}
	}

	out.WriteString(logState(engine))

	return out.String()
}

// logState is the part of the dump that reads the engine log: every cancel line, and the last warnings and errors.
func logState(engine *Engine) string {
	var out strings.Builder

	text, err := os.ReadFile(filepath.Join(engine.dir, logFileName))
	if err != nil {
		fmt.Fprintf(&out, "cannot read the engine log: %v\n", err)
	}

	var problems, cancels []string

	for line := range strings.Lines(string(text)) {
		if strings.Contains(line, "level=WARN") || strings.Contains(line, "level=ERROR") {
			problems = append(problems, line)
		}

		if strings.Contains(line, cancelStartedMarker) || strings.Contains(line, cancelEndedMarker) {
			cancels = append(cancels, line)
		}
	}

	fmt.Fprintf(&out, "engine log, %d cancel lines:\n%s", len(cancels), strings.Join(cancels, ""))
	fmt.Fprintf(&out, "engine log, %d warnings and errors, the last %d:\n%s", len(problems), min(len(problems), logLinesInDump), strings.Join(problems[max(0, len(problems)-logLinesInDump):], ""))

	return out.String()
}

// printStateIfTheTestFails prints the state of the engine at the end of the test when err is set and the test has failed. It reads the store before the cleanup that shuts the engine down.
// A test that passes prints nothing, so a test that expects an error stays quiet.
func printStateIfTheTestFails(t *testing.T, engine *Engine, err error, extra func() string) {
	t.Helper()

	if err == nil {
		return
	}

	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("the end of a run failed with: %v\n%s%s", err, extra(), within(dumpBound, func() string { return workflowState(engine) }))
		}
	})
}

// receiveEnd waits for the end of a queued gate. When the gate ends with an error and the test fails, the test prints the state of the engine.
func receiveEnd(t *testing.T, engine *Engine, g supersedeGate, ended <-chan gateEnd) gateEnd {
	t.Helper()

	end := receive(t, ended)
	printStateIfTheTestFails(t, engine, end.err, func() string {
		return fmt.Sprintf("gates started %d, stopped %d\n", g.started.Load(), g.stopped.Load())
	})

	return end
}

// reachCount waits until count holds want, the wanted number of gates that verb names. A count above want fails the wait at once: a gate that starts or stops twice is a defect,
// and a wait for an exact count would hang on it. The failure and the timeout of the wait carry the dump of the engine.
func reachCount(engine *Engine, g supersedeGate, count *atomic.Int32, want int32, verb string) waiting[struct{}] {
	return waiting[struct{}]{
		what: fmt.Sprintf("%d gates to have %s", want, verb),
		read: func() (struct{}, bool, error) {
			reached := count.Load()
			if reached > want {
				return struct{}{}, false, fmt.Errorf("%w: %d %s, want %d\n%s", errMoreGatesThanExpected, reached, verb, want, gateDump(engine, g))
			}

			return struct{}{}, reached == want, nil
		},
		last: func() string {
			return fmt.Sprintf("%d of %d gates %s\n%s", count.Load(), want, verb, gateDump(engine, g))
		},
	}
}

// startsReach waits until want gates have started.
func startsReach(t *testing.T, engine *Engine, g supersedeGate, want int32) waiting[struct{}] {
	t.Helper()

	return reachCount(engine, g, g.started, want, "started")
}

// startsOfRun is startsReach for a test that has a run under test: the wait also fails at once when the run ends or parks without the start.
func startsOfRun(t *testing.T, engine *Engine, g supersedeGate, want int32) waiting[struct{}] {
	t.Helper()

	wait := startsReach(t, engine, g, want)
	wait.run = runOfEngine(t, engine)

	return wait
}

// stopsReach waits until the context of want gates has stopped them.
func stopsReach(t *testing.T, engine *Engine, g supersedeGate, want int32) waiting[struct{}] {
	t.Helper()

	return reachCount(engine, g, g.stopped, want, "stopped")
}

// waitsInTheQueue is true when the queue holds want gates that wait for the gate that runs. A gate that is not yet queued cannot be stopped by a later gate.
func waitsInTheQueue(engine *Engine, want int) func() bool {
	return func() bool {
		rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName), dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued))

		return err == nil && len(rows) == want
	}
}

// endsAfterTicks ticks the engine until the gate ends, and returns how it ended. It ticks once more after the end: DBOS ends the run when the store marks the cancel,
// which can come before any tick that follows the mark, and only a tick after the mark cancels the context of a gate that runs.
func endsAfterTicks(t *testing.T, h *harness, ended <-chan gateEnd) gateEnd {
	t.Helper()

	end := waitOn(t, waiting[gateEnd]{what: "the gate to end", ch: ended, read: func() (gateEnd, bool, error) {
		h.tick(t)

		return gateEnd{}, false, nil
	}})

	h.tick(t)

	return end
}

func TestSuperseded_AGateThatRunsForAnOldHeadStopsWhenTheGateOfTheNewHeadIsQueued(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	old := queueGate(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))

	h.forge.mu.Lock()
	h.forge.state.Head = "B"
	h.forge.mu.Unlock()

	fresh := queueGate(engine, "B")

	end := endsAfterTicks(t, h, old)
	require.ErrorIs(t, end.err, errSuperseded)
	require.EqualError(t, end.err, "superseded by B")
	// The tick after the end has canceled the context of the gate. The gate counts its stop a moment later.
	waitOn(t, stopsReach(t, engine, gated, 1))

	waitOn(t, startsReach(t, engine, gated, 2))
	close(gated.release)

	newer := receive(t, fresh)
	require.NoError(t, newer.err)
	assert.Equal(t, true, newer.out.Outputs["passed"])
}

func TestSuperseded_AGateThatRunsForAnOldHeadStopsWhenItsRunEndedBeforeTheFirstTick(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	old := queueGate(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))

	h.forge.mu.Lock()
	h.forge.state.Head = "B"
	h.forge.mu.Unlock()

	fresh := queueGate(engine, "B")

	// Nothing ticks here, so the run of the old gate ends with no tick after the store marked its cancel, as it does on a host that is slow to tick.
	ended := make(chan gateEnd, 1)
	ended <- receive(t, old)

	require.ErrorIs(t, endsAfterTicks(t, h, ended).err, errSuperseded)
	waitOn(t, stopsReach(t, engine, gated, 1))
	waitOn(t, startsReach(t, engine, gated, 2))
	close(gated.release)

	require.NoError(t, receive(t, fresh).err)
}

func TestGateDump_ShowsTheCountsTheWorkflowsAndTheStepsOfARunningGate(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	ended := queueGate(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))

	dump := gateDump(engine, gated)

	for _, want := range []string{
		"gates started 1, stopped 0",
		fmt.Sprintf("name=%s status=PENDING queue=%q partition=%q", exclusiveName, exclusiveQueueName, testProject),
		"step 0 " + gateTipName,
		"step 0 " + gateJobName,
		"engine log, ",
	} {
		assert.Contains(t, dump, want)
	}

	close(gated.release)
	require.NoError(t, receive(t, ended).err)
}

func TestWorkflowState_ShowsTheTimesTheExecutorTheParentAndEveryCancelLineOfTheEngineLog(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	ended := queueGate(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))

	engine.logger.Info(cancelStartedMarker, "workflow_id", "gate-a")
	engine.logger.Info(cancelEndedMarker, "workflow_id", "gate-b")
	engine.logger.Info("Workflow started", "workflow_id", "gate-c")

	state := workflowState(engine)

	assert.Regexp(t, `workflow \S+ name=\S+ status=PENDING queue="[^"]*" partition="[^"]*" updated_at=\d\d:\d\d:\d\d\.\d{6} completed_at=- executor="[^"]*" parent=""`, state)
	assert.Regexp(t, `  step 0 \S+ started_at=\d\d:\d\d:\d\d\.\d{6} completed_at=\d\d:\d\d:\d\d\.\d{6} error=<nil>`, state)
	assert.Contains(t, state, "engine log, 2 cancel lines:")
	assert.Contains(t, state, fmt.Sprintf("msg=%q workflow_id=gate-a", cancelStartedMarker))
	assert.Contains(t, state, fmt.Sprintf("msg=%q workflow_id=gate-b", cancelEndedMarker))
	assert.NotContains(t, state, "gate-c", "an Info line that is no cancel line stays out")

	close(gated.release)
	require.NoError(t, receive(t, ended).err)
}

func TestStartsReach_AGateThatStartsTwiceFailsTheWaitAtOnce(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	first := queueGate(engine, "A")
	second := queueGateOf(engine, "other-project", testItem, "X")

	waitOn(t, startsReach(t, engine, gated, 2))

	_, _, err := startsReach(t, engine, gated, 1).read()
	require.ErrorIs(t, err, errMoreGatesThanExpected)
	require.ErrorContains(t, err, "2 started, want 1")
	assert.Contains(t, err.Error(), "gates started 2, stopped 0", "the failure carries the dump")

	close(gated.release)
	require.NoError(t, receive(t, first).err)
	require.NoError(t, receive(t, second).err)
}

func TestStopsReach_AGateThatStopsTwiceFailsTheWaitAtOnce(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	gated.stopped.Store(2)

	_, _, err := stopsReach(t, engine, gated, 1).read()
	require.ErrorIs(t, err, errMoreGatesThanExpected)
	require.ErrorContains(t, err, "2 stopped, want 1")
	assert.Contains(t, err.Error(), "gates started 0, stopped 2", "the failure carries the dump")
}

func TestReachCount_ATimeoutNamesTheCountAndPrintsTheDump(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	ended := queueGate(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))

	started := startsReach(t, engine, gated, 2).last()
	assert.Contains(t, started, "1 of 2 gates started")
	assert.Contains(t, started, "gates started 1, stopped 0", "the timeout carries the dump")
	assert.Contains(t, started, "status=PENDING")

	stopped := stopsReach(t, engine, gated, 1).last()
	assert.Contains(t, stopped, "0 of 1 gates stopped")
	assert.Contains(t, stopped, "gates started 1, stopped 0", "the timeout carries the dump")

	close(gated.release)
	require.NoError(t, receive(t, ended).err)
}

func TestGateDump_ShowsAGateThatIsQueuedAndHasNoSteps(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	running := queueGate(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))

	// The second gate has the same head, so it stops nothing and waits behind the first in the queue of the project.
	waiting := queueGate(engine, "A")

	waitOn(t, condition(waitsInTheQueue(engine, 1)))

	dump := gateDump(engine, gated)
	assert.Regexp(t, fmt.Sprintf(`status=ENQUEUED queue=%q partition=%q [^\n]*\n(?:workflow |engine log)`, exclusiveQueueName, testProject), dump, "a queued gate has a line and no step")

	close(gated.release)
	require.NoError(t, receive(t, running).err)
	require.NoError(t, receive(t, waiting).err)
}

func TestGateDump_NamesTheTimeoutWhenTheReadsDoNotFinishInTime(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	assert.Equal(t, "the dump did not finish in 1ns\n", gateDumpWithin(time.Nanosecond, engine, gated))
	assert.Contains(t, gateDumpWithin(time.Minute, engine, gated), "gates started 0, stopped 0")
}

func TestWithin_ReturnsTheTextOfAFinishedBuildAndNamesAnUnfinishedOne(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "text", within(time.Minute, func() string { return "text" }))

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	assert.Equal(t, "the dump did not finish in 1ms\n", within(time.Millisecond, func() string {
		<-block

		return "text"
	}))
}

func TestSuperseded_AGateThatWaitsInTheQueueForAnOldHeadNeverStarts(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	first := queueGate(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))

	// The pull request already has head C, so the gate of head B that queues now is late: it stops no gate and waits behind the gate of head A.
	h.forge.mu.Lock()
	h.forge.state.Head = "C"
	h.forge.mu.Unlock()

	waiting := queueGate(engine, "B")

	waitOn(t, condition(waitsInTheQueue(engine, 1)))

	last := queueGate(engine, "C")

	skipped := endsAfterTicks(t, h, waiting)
	require.ErrorIs(t, skipped.err, errSuperseded)
	require.EqualError(t, skipped.err, "superseded by C")

	require.ErrorIs(t, endsAfterTicks(t, h, first).err, errSuperseded)
	waitOn(t, startsReach(t, engine, gated, 2))
	close(gated.release)
	require.NoError(t, receive(t, last).err)
	assert.EqualValues(t, 2, gated.started.Load(), "the gate of head B never started")
}

func TestSuperseded_AGateOfTheSameHeadIsNotStopped(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	first := queueGate(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))

	secondRun, second := queueGateRun(engine, "A")

	// The second gate has recorded its stop of the old gates, and the first gate still runs.
	waitOn(t, condition(stopRecorded(engine, secondRun)))
	close(gated.release)

	require.NoError(t, receive(t, first).err)
	require.NoError(t, receive(t, second).err)
	assert.Zero(t, gated.stopped.Load())
}

func TestSuperseded_AGateOfAnOldHeadThatQueuesLateStopsNoGateOfTheCurrentHead(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "B")

	run, current := queueGateRun(engine, "B")

	// The gate of head B runs before the run that queued it records its stop of the old gates. A gate of head A that queues in between is an old gate to it.
	waitOn(t, startsReach(t, engine, gated, 1))
	waitOn(t, condition(stopRecorded(engine, run)))

	lateRun, late := queueGateRun(engine, "A")

	// The late gate has queued and recorded its own stop. A running gate stops at a tick of the engine, so the ticks come before the release.
	waitOn(t, condition(stopRecorded(engine, lateRun)))
	h.tick(t)
	assert.Zero(t, gated.stopped.Load(), "the gate of the current head is never stopped by a late gate")

	close(gated.release)

	require.NoError(t, receive(t, current).err)
	require.NoError(t, receive(t, late).err)
	assert.Zero(t, gated.stopped.Load())
}

func TestSuperseded_AGateThatNamesNoHeadTakesTheHeadOfThePullRequest(t *testing.T) {
	t.Parallel()

	standing := review.Standing{Head: "B"}

	assert.Equal(t, "B", tipOf(ExclusiveInput{Kind: kindGate}, standing, true))
	assert.Equal(t, "A", tipOf(ExclusiveInput{Kind: kindGate, Head: "A"}, standing, true))
	assert.Empty(t, tipOf(ExclusiveInput{Kind: kindGate}, standing, false), "a pull request that cannot be read gives no tip")
	assert.Empty(t, tipOf(ExclusiveInput{Kind: kindMerge, Head: "A"}, standing, true))
}

func TestSuperseded_AGateThatAPersonCancelsKeepsTheErrorOfTheCancel(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	only := queueGate(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName), dbos.WithFilterStatus(dbos.WorkflowStatusPending))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, dbos.CancelWorkflow(engine.ctx, rows[0].ID))

	end := endsAfterTicks(t, h, only)
	require.ErrorIs(t, end.err, dbos.ErrAwaitedWorkflowCancelled)
	require.NotErrorIs(t, end.err, errSuperseded)
}

func TestSuperseded_AStoreThatCannotBeListedLeavesTheOldGatesAloneAndSaysSo(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "B"}

	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	var logged bytes.Buffer

	engine.logger = newLogger(&logged, runRedactor(nil))

	stopped := engine.stopSuperseded(t.Context(), ExclusiveInput{Kind: kindGate, Project: testProject, Item: testItem, Tip: "B"})

	assert.Empty(t, stopped)
	assert.Contains(t, logged.String(), "cannot stop the old gates of the pull request")
}

func TestSuperseded_AQueuedRunThatIsNotAGateIsNotRead(t *testing.T) {
	t.Parallel()

	_, ok := gateInputOf(`{"kind":"merge","project":"p","item":"12","tip":"A"}`)
	assert.False(t, ok)

	_, ok = gateInputOf("not json")
	assert.False(t, ok)

	_, ok = gateInputOf(42)
	assert.False(t, ok)
}

func TestSuperseded_ACancelledGateIsNeverReusedAsAPass(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	old := queueGate(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))

	h.forge.mu.Lock()
	h.forge.state.Head = "B"
	h.forge.mu.Unlock()

	_ = queueGate(engine, "B")

	require.ErrorIs(t, endsAfterTicks(t, h, old).err, errSuperseded)

	key := GateKey{ID: "key-1", Head: "A", Tree: "tree-1"}
	_, found := engine.earlierPass(key, testProject)
	assert.False(t, found, "only a run that finished with a pass is read for reuse")
}
