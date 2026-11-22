package flow

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// holdItems makes the children in held wait on the returned channel, or on the end of their run. Closing the channel lets them go on.
func holdItems(h *harness, held ...string) chan struct{} {
	release := make(chan struct{})

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		for _, item := range held {
			if request.Item != item {
				continue
			}

			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		return nil
	}

	return release
}

// stepJSON returns the recorded output of the named step of the test run.
func stepJSON(t *testing.T, engine *Engine, name string) string {
	t.Helper()

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	for _, s := range steps {
		if s.StepName == name {
			text, isText := s.Output.(string)
			require.True(t, isText, "the step output is stored as text")

			return text
		}
	}

	t.Fatalf("no step %s", name)

	return ""
}

// firstFile is a parent that spawns three children and calls first_of on them with the arguments in call. Its command step names what the call returned.
func firstFile(call string) string {
	return `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    e = ` + call + `
    command(name = "after", run = ["after", e.kind, str(e.run.item if e.run else None), str(e.outcome.ok if e.outcome else None), str(e.message)])
`
}

func TestFirstOf_ReturnsTheFirstRunThatEndedAndRecordsIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21", "23")

	defer close(release)

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, firstFile(`first_of(runs = runs, name = "fan")`)))

	require.NoError(t, err)
	assert.Equal(t, []string{"after run 22 True None"}, h.commands.ranBy(testItem), "only the run that ended came back")
	assert.JSONEq(t, `{"kind":"run","ended":[{"run":{"item":"22","run":"item-22-r1","login":"alice","priority":6,"start":true},"outcome":{"item":"22","ok":true,"cost_usd":0,"attempts":1}}]}`, stepJSON(t, engine, "first_of:fan#1"))
}

func TestFirstOf_ARunThatAnEarlierCallReturnedDoesNotWinAgain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    gather(runs, name = "all")
    seen = [r.item for r in first_of(runs = runs, name = "n0").runs]
    again = first_of(runs = runs, timeout = "1ms", name = "n1")
    command(name = "after", run = ["after", again.kind] + seen)
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file))

	require.NoError(t, err)
	assert.Equal(t, []string{"after timer 21 22 23"}, h.commands.ranBy(testItem), "one call returns the three ended runs, and a second call finds none left")
}

func TestGather_TheDefaultWaitsForEveryRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "23")
	engine := h.engine(t)
	done := make(chan error, 1)
	file := writeFlow(t, swarmFile(""))

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		done <- err
	}()

	waitOn(t, conditionOf(runOfEngine(t, engine), func() bool { return itemEnded(engine, "21") && itemEnded(engine, "22") }))
	time.Sleep(10 * tickInterval)
	assert.Empty(t, h.commands.ranBy(testItem), "the parent still waits for run 23")
	close(release)
	require.NoError(t, receive(t, done))
	assert.Equal(t, []string{"after 21:ok 22:ok 23:ok"}, h.commands.ranBy(testItem))
}

// interruptedAtTheQuestion runs the parent until it asks its question, lets the held runs go and end, and then stops the engine. It differs from interrupted in that the status lists the child runs too.
func interruptedAtTheQuestion(t *testing.T, h *harness, file string, release chan struct{}) {
	t.Helper()

	stopCtx, stop := context.WithCancel(t.Context())
	engine, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	client := h.client(t, testVersion)

	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file)
		ended <- runErr
	}()

	waitOn(t, waiting[struct{}]{what: "the parent to show its question", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		rows, statusErr := client.Status()

		return struct{}{}, statusErr == nil && slices.ContainsFunc(rows, func(r RunInfo) bool { return r.Question != "" }), nil
	}})

	close(release)
	waitOn(t, condition(func() bool { return itemEnded(engine, "21") && itemEnded(engine, "23") }))

	stop()
	require.NoError(t, engine.Shutdown())
}

func TestFirstOf_AResumeOfTheParentReturnsTheRecordedWinnerNotAFreshRead(t *testing.T) {
	t.Parallel()

	file := writeFlow(t, `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    done = first_of(runs = runs, name = "fan")
    wait(name = "ask", question = "go?")
    command(name = "after", run = ["after", done.outcome.item + ":" + ("ok" if done.outcome.ok else done.outcome.error)])
`)
	h := newHarness(t)
	release := holdItems(h, "21", "23")

	interruptedAtTheQuestion(t, h, file, release)

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the answer to be accepted", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"after 22:ok"}, h.commands.ranBy(testItem), "the winner recorded before the stop comes back, whatever the runs say now")
}

func TestGather_TheKeywordUntilIsGone(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow("gather([], until = \"any\")")))

	require.ErrorContains(t, err, "gather takes no argument until")
}

func TestFirstOf_AnEndNoticeWakesTheParentLongBeforeTheWindowEnds(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.window = time.Hour

	engine := h.engine(t)
	done := startRun(t, engine, writeFlow(t, firstFile(`first_of(runs = runs, name = "fan")`)))

	require.NoError(t, parentEnds(t, done), "a window of one hour would hold the parent, so only the end notice wakes it")
}

// parentEnds waits for the end of the test run, and fails with a named failure when the run is still open after the bound of the wait. It has no stall check: the parent of these tests legitimately sleeps in a wait that only a notice wakes.
func parentEnds(t *testing.T, done <-chan runOutcome) error {
	t.Helper()

	return waitOn(t, waiting[runOutcome]{what: "the parent run to end", ch: done}).err
}

func TestFirstOf_ATurnThatEndsWithNoEventReadsTheRunsAgain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.window = 50 * time.Millisecond

	release := holdItems(h, "21", "22", "23")
	engine := h.engine(t)
	done := make(chan error, 1)
	file := writeFlow(t, firstFile(`first_of(runs = runs, name = "fan")`))

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		done <- err
	}()

	// No run ends and no notice comes, so only the end of the window starts another turn. Three recorded reads prove that the turns go on without an event.
	waitOn(t, conditionOf(runOfEngine(t, engine), func() bool { return looksOf(t, engine, "first_of:fan#1") >= 3 }))
	close(release)
	require.NoError(t, receive(t, done))
	require.Len(t, h.commands.ranBy(testItem), 1)
	assert.Contains(t, h.commands.ranBy(testItem)[0], "after run 2")
}

// looksOf counts the recorded reads of the first_of step of the test run.
func looksOf(t *testing.T, engine *Engine, step string) int {
	t.Helper()

	return len(slices.DeleteFunc(stepNamesOf(t, engine, testRun), func(name string) bool { return !strings.HasPrefix(name, step+lookSuffix) }))
}

func TestFirstOf_AResumeUsesTheDeadlineThatTheFirstCallRecordedNotAFreshOne(t *testing.T) {
	t.Parallel()

	var skew atomic.Int64

	h := newHarness(t)
	h.reading = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }

	release := holdItems(h, "21", "22", "23")

	defer close(release)

	file := writeFlow(t, firstFile(`first_of(runs = runs, timeout = "1h", name = "fan")`))

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	_ = startRun(t, first, file)
	deadline := "first_of:fan#1" + deadlineSuffix

	waitOn(t, conditionOf(runOfEngine(t, first), func() bool { return slices.Contains(stepNamesOf(t, first, testRun), deadline) }))
	stop()
	require.NoError(t, first.Shutdown())

	// The clock of the second run reads two hours later. The recorded deadline, one hour after the first call began, has passed, so the call returns the timer.
	// A call that made a new deadline would make it three hours after the first call began, and would wait on.
	skew.Store(int64(2 * time.Hour))

	next := h.engine(t)
	done := startRun(t, next, file)

	require.NoError(t, parentEnds(t, done))
	assert.Equal(t, []string{"after timer None None None"}, h.commands.ranBy(testItem), "the resumed call used the deadline of the first call")
}

func TestFirstOf_ACancelFromTheCommandLineWakesTheParentLongBeforeTheWindowEnds(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.window = time.Hour

	release := holdItems(h, "21", "22", "23")

	defer close(release)

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := startRun(t, engine, writeFlow(t, firstFile(`first_of(runs = runs, name = "fan")`)))

	waitOn(t, waiting[struct{}]{what: "the child run to be canceled", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		return struct{}{}, client.CancelRun("item-21-r1") == nil, nil
	}})

	require.NoError(t, parentEnds(t, done))
	assert.Equal(t, []string{"after run 21 False None"}, h.commands.ranBy(testItem), "the cancel told the parent, which read the end at once")
}

func TestFirstOf_TheTimeoutReturnsTheTimerWhileTheRunsAreOpen(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21", "22", "23")

	defer close(release)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, firstFile(`first_of(runs = runs, timeout = "1s", name = "fan")`)))

	require.NoError(t, err)
	assert.Equal(t, []string{"after timer None None None"}, h.commands.ranBy(testItem))
}

func TestFirstOf_AMessageOfAnotherCallStaysForItsOwnCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21", "22", "23")

	defer close(release)

	file := `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    stop = first_of(runs = runs, topics = ["stop"], name = "one")
    go = first_of(runs = runs, topics = ["go"], timeout = "5s", name = "two")
    command(name = "after", run = ["after", stop.kind, str(stop.message), go.kind, str(go.message)])
`
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan error, 1)
	path := writeFlow(t, file)

	go func() {
		_, err := engine.RunFile(testItem, testProject, path)
		done <- err
	}()

	waitOn(t, waiting[struct{}]{what: "the messages to be accepted", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		if _, err := client.Send(testItem, Outgoing{Topic: "go", Key: "k1", Value: []byte(`"g"`)}); err != nil {
			return struct{}{}, false, nil
		}

		_, err := client.Send(testItem, Outgoing{Topic: "stop", Key: "k2", Value: []byte(`"s"`)})

		return struct{}{}, err == nil, nil
	}})

	require.NoError(t, receive(t, done))
	assert.Equal(t, []string{"after message s message g"}, h.commands.ranBy(testItem), "the go message waited in the buffer until a call named its topic")
}

func TestFirstOf_ACallWithNothingLeftToWaitForIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21"], entry = "worker", name = "fan")
    first_of(runs = runs, name = "one")
    first_of(runs = runs, name = "two")
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file))

	require.ErrorContains(t, err, "every run of first_of was returned by an earlier call")
}

func TestFirstOf_ARunThatIsNotAChildOfThisRunIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(`first_of(runs = [struct(item = "9", run = "item-9-r1")], name = "x")`)))

	require.Error(t, err)
}

func TestFirstOf_TheOrderOfTheListDecidesWhichEndedRunComesFirst(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    gather(runs, name = "all")
    ordered = [runs.runs[2], runs.runs[1], runs.runs[0]]
    got = first_of(runs = ordered, name = "one")
    command(name = "after", run = ["after", got.run.item] + [r.item for r in got.runs])
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 23 23 22 21"}, h.commands.ranBy(testItem), "a reversed list reverses the order in which the ended runs come, and run is the first of them")
}

func TestFirstOf_ARunThatTheListNamesTwiceComesOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22"], entry = "worker", name = "fan")
    gather(runs, name = "all")
    twice = runs.runs + runs.runs
    seen = [r.item for r in first_of(runs = twice, name = "one").runs]
    command(name = "after", run = ["after"] + seen)
    first_of(runs = twice, name = "two")
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file))

	require.ErrorContains(t, err, "every run of first_of was returned by an earlier call")
	assert.Equal(t, []string{"after 21 22"}, h.commands.ranBy(testItem), "two runs named twice are two events, not four")
}

func TestFirstOf_ATopicThatOnlyContainsTheNameIsNotThatTopic(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21", "22", "23")

	defer close(release)

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan error, 1)
	path := writeFlow(t, firstFile(`first_of(runs = runs, topics = ["stop"], timeout = "3s", name = "fan")`))

	go func() {
		_, err := engine.RunFile(testItem, testProject, path)
		done <- err
	}()

	waitOn(t, waiting[struct{}]{what: "the message to be accepted", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		_, err := client.Send(testItem, Outgoing{Topic: "stopper", Key: "k1", Value: []byte(`"x"`)})

		return struct{}{}, err == nil, nil
	}})

	require.NoError(t, receive(t, done))
	assert.Equal(t, []string{"after timer None None None"}, h.commands.ranBy(testItem), "the topic stopper is not the topic stop")
}

// holdOneAndPauseTheParent holds the child of item 23 until the returned channel closes. The command pause of the parent waits until the children of items 21 and 22 ended, so the first read of
// the parent after it finds both ended. The hook reads the store through the engine that current holds.
func holdOneAndPauseTheParent(h *harness, current *atomic.Pointer[Engine]) chan struct{} {
	release := make(chan struct{})

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		switch {
		case request.Item == "23":
			return waitFor(ctx, func() bool { return isClosed(release) })
		case request.Item == testItem && slices.Equal(request.Run, []string{"pause"}):
			return waitFor(ctx, func() bool { return itemEnded(current.Load(), "21") && itemEnded(current.Load(), "22") })
		}

		return nil
	}

	return release
}

// waitFor polls until done is true or the context ends.
func waitFor(ctx context.Context, done func() bool) error {
	for !done() {
		select {
		case <-time.After(storePoll):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

const runsFile = `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    command(name = "pause", run = ["pause"])
    e = first_of(runs = runs, name = "fan")
    WAIT
    command(name = "after", run = ["after", e.kind, e.run.item, ",".join([r.item + ":" + str(r.outcome.ok) for r in e.runs]), str(len(e.runs))])
`

func TestFirstOf_RunsHoldsEveryRunThatEndedInTheReadAndRunIsTheFirstOfThem(t *testing.T) {
	t.Parallel()

	var current atomic.Pointer[Engine]

	h := newHarness(t)
	release := holdOneAndPauseTheParent(h, &current)

	defer close(release)

	engine := h.engine(t)
	current.Store(engine)

	done := startRun(t, engine, writeFlow(t, strings.Replace(runsFile, "    WAIT\n", "", 1)))

	require.NoError(t, parentEnds(t, done))
	assert.Equal(t, []string{"pause", "after run 21 21:True,22:True 2"}, h.commands.ranBy(testItem), "one call returns the two runs that ended in the read, and the run of item 23 is still open")
}

func TestFirstOf_ARunThatACallReturnedInRunsDoesNotWinAgain(t *testing.T) {
	t.Parallel()

	var current atomic.Pointer[Engine]

	h := newHarness(t)
	release := holdOneAndPauseTheParent(h, &current)

	defer close(release)

	engine := h.engine(t)
	current.Store(engine)

	done := startRun(t, engine, writeFlow(t, strings.Replace(runsFile, "    WAIT\n", "    again = first_of(runs = runs, timeout = \"1ms\", name = \"again\")\n    if again.kind != \"timer\" or len(again.runs) != 0:\n        fail(\"the runs of the first call came again\")\n", 1)))

	require.NoError(t, parentEnds(t, done))
	assert.Equal(t, []string{"pause", "after run 21 21:True,22:True 2"}, h.commands.ranBy(testItem))
}

func TestFirstOf_AResumeAfterTheReadReturnsTheRunsThatThatReadRecordedNotAFreshRead(t *testing.T) {
	t.Parallel()

	var current atomic.Pointer[Engine]

	h := newHarness(t)
	release := holdOneAndPauseTheParent(h, &current)
	file := writeFlow(t, strings.Replace(runsFile, "    WAIT\n", "    wait(name = \"ask\", question = \"go?\")\n", 1))

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())
	current.Store(first)

	_ = startRun(t, first, file)
	client := h.client(t, testVersion)

	waitOn(t, waiting[struct{}]{what: "the parent to show its question", run: runOfEngine(t, first), read: func() (struct{}, bool, error) {
		rows, statusErr := client.Status()

		return struct{}{}, statusErr == nil && slices.ContainsFunc(rows, func(r RunInfo) bool { return r.Question != "" }), nil
	}})

	// The run of item 23 ends after the read. A fresh read on the resume would list it with the others.
	close(release)
	waitOn(t, condition(func() bool { return itemEnded(first, "23") }))
	stop()
	require.NoError(t, first.Shutdown())

	next := h.engine(t)
	current.Store(next)

	done := startRun(t, next, file)

	waitOn(t, waiting[struct{}]{what: "the answer to be accepted", run: runOfEngine(t, next), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})
	require.NoError(t, parentEnds(t, done))
	assert.Equal(t, []string{"pause", "after run 21 21:True,22:True 2"}, h.commands.ranBy(testItem), "the runs that the recorded read held come back, not the three runs that ended since")
}
