package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swarmFile is a parent that spawns three children, gathers them with the given arguments and records what it saw in a last command.
func swarmFile(gatherArgs string) string {
	return `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    done = gather(runs, ` + gatherArgs + `name = "fan")
    command(name = "after", run = ["after"] + [r.item + ":" + ("ok" if r.ok else r.error) for r in done.results])
`
}

// ranBy lists the command lines that ran for one item.
func (c *fakeCommands) ranBy(item string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []string

	for _, request := range c.requests {
		if request.Item == item {
			out = append(out, strings.Join(request.Run, " "))
		}
	}

	return out
}

// failItem makes every command of one item fail while on is true.
func failItem(h *harness, item string, on *atomic.Bool) {
	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Item == item && on.Load() {
			return errFake
		}

		return nil
	}
}

// childStatus returns the status of the latest run of an item.
func childStatus(t *testing.T, engine *Engine, item string) dbos.WorkflowStatusType {
	t.Helper()

	last, found, err := latestRun(engine.ctx, item)
	require.NoError(t, err)
	require.True(t, found, "item %s has no run", item)

	return last.Status.Status
}

func stepNamesOf(t *testing.T, engine *Engine, id string) []string {
	t.Helper()

	steps, err := dbos.GetWorkflowSteps(engine.ctx, id)
	require.NoError(t, err)

	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.StepName
	}

	return names
}

func TestSpawnGather_ThreeChildrenSucceedAndTheParentSeesEachResultInOrder(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, swarmFile("")))

	require.NoError(t, err)

	for _, item := range []string{"21", "22", "23"} {
		assert.Equal(t, []string{"w"}, h.commands.ranBy(item), "item %s ran its entry once", item)
		assert.Equal(t, dbos.WorkflowStatusSuccess, childStatus(t, engine, item))
	}

	assert.Equal(t, []string{"after 21:ok 22:ok 23:ok"}, h.commands.ranBy(testItem))
	assert.Equal(t, []string{"spawn:fan#1", "gather:fan#1", "command:after#1"}, stepNames(t, engine))
	assert.NotContains(t, stepNamesOf(t, engine, testRun), "DBOS.getResult", "gather reads the rows and calls no GetResult")

	last, _, err := latestRun(engine.ctx, "21")
	require.NoError(t, err)
	assert.Equal(t, testRun, last.Status.ParentWorkflowID, "the run is a child of the parent")
}

func TestSpawnGather_AFailedChildIsDataWithFailReturnAndItsErrorPassesTheRedactor(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var failing atomic.Bool

	failing.Store(true)
	failItem(h, "22", &failing)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, swarmFile(`fail = "return", `)))

	require.NoError(t, err, "the parent goes on with the failed child as data")

	after := h.commands.ranBy(testItem)
	require.Len(t, after, 1)
	assert.Contains(t, after[0], "21:ok 22:")
	assert.Contains(t, after[0], errFake.Error())
	assert.Contains(t, after[0], "23:ok")
}

func TestSpawnGather_FailRaiseCancelsTheOpenChildrenAndRaisesAfterTheStep(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	started := make(chan struct{})

	var once atomic.Bool

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		switch request.Item {
		case "22":
			<-started

			return errFake
		default:
			if once.CompareAndSwap(false, true) {
				close(started)
			}

			<-ctx.Done()

			return ctx.Err()
		}
	}

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, swarmFile(`fail = "raise", `)))

	require.ErrorContains(t, err, "item 22 did not finish")
	require.ErrorContains(t, err, errFake.Error())
	assert.Empty(t, h.commands.ranBy(testItem), "the command after the gather never ran")
	assert.Equal(t, dbos.WorkflowStatusError, childStatus(t, engine, "22"))
	assert.Equal(t, dbos.WorkflowStatusCancelled, childStatus(t, engine, "21"))
	assert.Equal(t, dbos.WorkflowStatusCancelled, childStatus(t, engine, "23"))
	ended := `"error":"the run ended as ` + string(dbos.WorkflowStatusCancelled) + `"`

	assert.JSONEq(t, `{"outcomes":[{"item":"21","ok":false,`+ended+`},{"item":"22","ok":false,"error":"workflow.star:2:12: fake failure"},`+
		`{"item":"23","ok":false,`+ended+`}],"canceled":["item-21-r1","item-23-r1"],"failed":"item 22 did not finish: workflow.star:2:12: fake failure. Run obee resume 12 to reopen item 22 and run the spawn again"}`,
		gatherJSON(t, engine))

	h.tick(t)
	eventually(t, func() bool { return len(h.life.releasedBy()) >= 1 })
}

// gatherJSON returns the recorded output of the gather step of the parent.
func gatherJSON(t *testing.T, engine *Engine) string {
	t.Helper()

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	for _, s := range steps {
		if s.StepName == "gather:fan#1" {
			text, isText := s.Output.(string)
			require.True(t, isText, "the step output is stored as text")

			var out gathered
			require.NoError(t, json.Unmarshal([]byte(text), &out))

			data, marshalErr := json.Marshal(out)
			require.NoError(t, marshalErr)

			return string(data)
		}
	}

	t.Fatal("no gather step")

	return ""
}

func TestSpawnGather_ResumeOfAFailedParentReopensTheMidListChildAndEachOutputIsItsOwn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var (
		failing atomic.Bool
		running atomic.Pointer[Engine] // set once the engine exists; the hook reads it
	)

	failing.Store(true)

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Item != "22" || !failing.Load() {
			return nil
		}

		// Item 22 fails only when 21 and 23 ended, so the raise cancels no sibling. The test of the canceled sibling covers the other order.
		for !itemEnded(running.Load(), "21") || !itemEnded(running.Load(), "23") {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}

		return errFake
	}

	h.commands.reply = ownOutput

	engine := h.engine(t)
	running.Store(engine)
	client := h.client(t, testVersion)
	file := writeFlow(t, stopSwarm)

	_, err := engine.RunFile(testItem, testProject, file)
	require.ErrorContains(t, err, "item 22 did not finish")
	require.ErrorContains(t, err, "Run obee resume 12 to reopen item 22 and run the spawn again")

	failed := gatherJSON(t, engine)

	assert.Contains(t, failed, `"item":"22","ok":false`)
	assert.Equal(t, dbos.WorkflowStatusError, childStatus(t, engine, testItem))

	// The retry route is obee resume of the parent: it reopens the failed child and forks the parent from the step that started it.
	failing.Store(false)
	require.NoError(t, client.Resume(testItem))
	if !waitUntil(func() bool { return childStatus(t, engine, testItem) == dbos.WorkflowStatusSuccess }) {
		t.Fatal(dumpRuns(engine, testItem, "21", "22", "23"))
	}

	assert.JSONEq(t, `{"outcomes":[`+
		`{"item":"21","ok":true,"stopped":{"step":"command:w#1","rc":2,"output":"out-21"}},`+
		`{"item":"22","ok":true,"stopped":{"step":"command:w#1","rc":2,"output":"out-22"}},`+
		`{"item":"23","ok":true,"stopped":{"step":"command:w#1","rc":2,"output":"out-23"}}]}`, gatherJSON(t, engine), "each output is its own")
	assert.Equal(t, []string{"after 21:out-21 22:out-22 23:out-23"}, h.commands.ranBy(testItem))
	h.life.mu.Lock()
	assert.Equal(t, 2, h.life.takes["22"], "the reopened child holds a fresh claim")
	assert.Equal(t, 1, h.life.takes["21"], "a finished child takes no claim")
	h.life.mu.Unlock()
	assert.Len(t, h.commands.ranBy("21"), 1, "a finished child does not run again")
	assert.Len(t, h.commands.ranBy("23"), 1, "a finished child does not run again")
	assert.NotContains(t, stepNamesOf(t, engine, testRun), "DBOS.getResult")
}

func TestSpawnGather_ResumeOfAFailedParentReopensASiblingThatTheRaiseCancelled(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var (
		failing atomic.Bool
		running atomic.Pointer[Engine] // set once the engine exists; the hook reads it
	)

	failing.Store(true)

	// Item 22 fails once item 21 ended with success (a command that ran is not a child that ended). Item 23 stays open until the raise cancels it, so the cancel is certain and not a matter of timing.
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		switch {
		case !failing.Load():
			return nil
		case request.Item == "22":
			for !itemEnded(running.Load(), "21") {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Millisecond):
				}
			}

			return errFake
		case request.Item == "23":
			<-ctx.Done()

			return ctx.Err()
		}

		return nil
	}

	h.commands.reply = ownOutput

	engine := h.engine(t)
	running.Store(engine)
	client := h.client(t, testVersion)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, stopSwarm))
	require.ErrorContains(t, err, "item 22 did not finish")

	last, found, err := latestRun(engine.ctx, "23")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, dbos.WorkflowStatusCancelled, last.Status.Status, "the raise canceled the open sibling")

	require.Contains(t, gatherJSON(t, engine), `"canceled":["item-23-r1"]`, "the gather recorded the run that the raise canceled")

	steps, err := dbos.GetWorkflowSteps(client.flow, testRun, dbos.WithStepsLoadOutput(true))
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"item-23-r1": true}, raised(steps), "a resume reads the ids from the recorded gather step")

	failing.Store(false)
	require.NoError(t, client.Resume(testItem))

	if !waitUntil(func() bool { return childStatus(t, engine, testItem) == dbos.WorkflowStatusSuccess }) {
		t.Fatal(dumpRuns(engine, testItem, "21", "22", "23"))
	}

	assert.Equal(t, []string{"after 21:out-21 22:out-22 23:out-23"}, h.commands.ranBy(testItem))
	assert.Equal(t, dbos.WorkflowStatusSuccess, childStatus(t, engine, "23"), "the canceled sibling ran again")
}

// itemEnded reports whether the latest run of an item ended with success. A missing run is not ended.
func itemEnded(engine *Engine, item string) bool {
	last, found, err := latestRun(engine.ctx, item)

	return err == nil && found && last.Status.Status == dbos.WorkflowStatusSuccess
}

// countEnqueued counts the items whose latest run waits in a queue.
func countEnqueued(engine *Engine, items []string) int {
	count := 0

	for _, item := range items {
		last, found, err := latestRun(engine.ctx, item)
		if err == nil && found && last.Status.Status == dbos.WorkflowStatusEnqueued {
			count++
		}
	}

	return count
}

// receivedWithin reports whether the channel closes before eventTimeout passes.
func receivedWithin(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(eventTimeout):
		return false
	}
}

// waitUntil polls check until it returns true, and reports false when eventTimeout passes.
func waitUntil(check func() bool) bool {
	deadline := time.Now().Add(eventTimeout)

	for time.Now().Before(deadline) {
		if check() {
			return true
		}

		time.Sleep(pollEvery)
	}

	return false
}

// dumpRuns describes the parent row (status, error, attempt), its steps with child ids and the row of each child, for a failed wait.
func dumpRuns(engine *Engine, parent string, children ...string) string {
	var out strings.Builder

	describe := func(item string) {
		last, found, err := latestRun(engine.ctx, item)
		if err != nil || !found {
			out.WriteString("item " + item + ": no run: " + fmt.Sprint(err) + "\n")

			return
		}

		fmt.Fprintf(&out, "item %s: id=%s status=%s error=%v attempt=%d queue=%s\n", item, last.Status.ID, last.Status.Status, last.Status.Error, last.Attempt, last.Status.QueueName)

		steps, stepsErr := dbos.GetWorkflowSteps(engine.ctx, last.Status.ID)
		if stepsErr != nil {
			out.WriteString("  steps: " + stepsErr.Error() + "\n")

			return
		}

		for _, s := range steps {
			fmt.Fprintf(&out, "  step %d %s child=%q error=%v\n", s.StepID, s.StepName, s.ChildWorkflowID, s.Error)
		}
	}

	describe(parent)

	for _, child := range children {
		describe(child)
	}

	return out.String()
}

// stopSwarm is a parent that gathers three children with fail="raise". Each child stops on a code and prints its own item, so a parent can tell whose output it reads.
const stopSwarm = `def worker(item):
    command(name = "w", run = ["w"], stop_on_rc = [2])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    done = gather(runs, fail = "raise", name = "fan")
    command(name = "after", run = ["after"] + [r.item + ":" + r.stopped.output for r in done.results])
`

// ownOutput answers a command of a child with a stop code and the item as output, and a command of the parent with success.
func ownOutput(request CommandRequest) (CommandOutput, error) {
	if request.Item == testItem {
		return CommandOutput{}, nil
	}

	return CommandOutput{Stdout: "out-" + request.Item, RC: 2}, nil
}

// startOpenRun starts the item under the project in the background, with a flow that waits for the owner, and returns when the run is open.
func startOpenRun(t *testing.T, h *harness, engine *Engine, item, project string) {
	t.Helper()

	h.flowIs(t, waitFlow)

	file := filepath.Join(filepath.Dir(h.opts.StateDir), defaultFlowName)
	done := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(item, project, file)
		done <- err
	}()

	eventually(t, func() bool {
		last, found, err := latestRun(engine.ctx, item)

		return err == nil && found && !last.terminal()
	})
}

func TestSpawnGather_SpawnRefusesAnItemWithAnOpenRunAndNamesHowToEndIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	startOpenRun(t, h, engine, "21", testProject)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, swarmFile("")))

	require.ErrorContains(t, err, "item 21 has an open run, item-21-r1. Run obee status to see it, or obee cancel 21 to end it, then start this item again")
	assert.Empty(t, h.commands.ranBy("22"), "no child started")
}

func TestSpawnGather_SpawnRefusesAnItemWithAnOpenRunUnderAnotherProject(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	startOpenRun(t, h, engine, "21", "other")

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, swarmFile("")))

	require.ErrorIs(t, err, errProjectConflict)
	require.ErrorContains(t, err, "project other")
}

func TestSpawnGather_SpawnRefusesAnItemWhoseLastAttemptFailedAndNamesResume(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Item == "21" {
			return errFake
		}

		return nil
	}

	engine := h.engine(t)

	_, err := engine.RunFile("21", testProject, writeFlow(t, bodyFlow(`command(name = "w", run = ["w"])`)))
	require.Error(t, err)

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, swarmFile("")))

	require.ErrorIs(t, err, errRunFailed)
	require.ErrorContains(t, err, "Run obee resume 21 to continue it from the last finished step")
	assert.Empty(t, h.commands.ranBy("22"), "no child started")
}

func TestSpawnGather_SpawnReusesAChildThatFinishedAndStartsNoSecondAttempt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = ownOutput

	engine := h.engine(t)

	_, err := engine.RunFile("21", testProject, writeFlow(t, "def main(item):\n    command(name = \"w\", run = [\"w\"], stop_on_rc = [2])\n"))
	require.NoError(t, err)

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, stopSwarm))

	require.NoError(t, err)
	assert.Len(t, h.commands.ranBy("21"), 1, "the finished child did not run again")
	assert.Equal(t, []string{"after 21:out-21 22:out-22 23:out-23"}, h.commands.ranBy(testItem))

	last, _, err := latestRun(engine.ctx, "21")
	require.NoError(t, err)
	assert.Equal(t, 1, last.Attempt, "no second attempt")
}

func TestSpawnGather_ARefusedCallNamesTheProblemAndTheFix(t *testing.T) {
	t.Parallel()

	worker := "def worker(item):\n    spawn(items = [\"31\"], entry = \"worker\")\n\n"

	cases := map[string]struct{ file, want string }{
		"bad id":          {bodyFlow(`spawn(items = ["!!"])`), "invalid item"},
		"itself":          {bodyFlow(`spawn(items = ["12"])`), "item 12 cannot spawn itself. Leave it out of items"},
		"twice":           {bodyFlow(`spawn(items = ["21", "21"])`), "item 21 is in items twice. List each item once"},
		"not a list":      {bodyFlow(`spawn(items = 7 + 0)`), "items must be a list of strings"},
		"not strings":     {bodyFlow(`spawn(items = [7])`), "items must hold strings only"},
		"no such entry":   {bodyFlow("name = \"no\" + \"pe\"\nspawn(items = [\"21\"], entry = name)"), "entry \"nope\" is not a function of the workflow file. Add def nope(item)"},
		"bad fail":        {bodyFlow("mode = \"x\" + \"\"\ngather([], fail = mode)"), "`fail` is \"x\". Use \"return\" or \"raise\""},
		"runs not a list": {bodyFlow(`gather(1)`), "runs must be the value that spawn returned"},
		"run not struct":  {bodyFlow(`gather([1])`), "runs must hold the runs that spawn returned, and 1 is a int"},
		"run no fields":   {bodyFlow(`gather([struct(item = "1")])`), "has no item and run"},
		"run text":        {bodyFlow(`gather([struct(item = 1, run = 2)])`), "has no item and run"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, c.file))

			require.ErrorContains(t, err, c.want)
		})
	}

	t.Run("a child cannot spawn", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		flow := worker + "def main(item):\n    done = gather(spawn(items = [\"21\"], entry = \"worker\"))\n    command(name = \"after\", run = [\"after\", done.results[0].error])\n"
		_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow))

		require.NoError(t, err)
		require.Len(t, h.commands.ranBy(testItem), 1)
		assert.Contains(t, h.commands.ranBy(testItem)[0], "item 21 was started by spawn and cannot start items. Call spawn from the item that a user started")
	})
}

func TestSpawnGather_ARunThatIsNotInTheStoreIsAFailedOutcomeAndAnEmptyCallReturnsAtOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	flow := bodyFlow("none = gather(spawn(items = []))\ndone = gather([struct(item = \"9\", run = \"item-9-r1\")])\ncommand(name = \"after\", run = [\"after\", str(len(none.results)), done.results[0].error])")

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 0 run item-9-r1 is not in the store"}, h.commands.ranBy(testItem))
}

func TestSpawnGather_FiveChildrenRunAtMostMaxConcurrentAtOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var (
		running, peak atomic.Int32
		full          = make(chan struct{}) // closed when two children run at once
		release       = make(chan struct{}) // closed by the test, to let the children end
		once          sync.Once
	)

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Item == testItem {
			return nil
		}

		now := running.Add(1)
		defer running.Add(-1)

		for seen := peak.Load(); now > seen && !peak.CompareAndSwap(seen, now); seen = peak.Load() {
		}

		if now == 2 {
			once.Do(func() { close(full) })
		}

		select {
		case <-release:
		case <-ctx.Done():
		}

		return nil
	}

	flow := "def worker(item):\n    command(name = \"w\", run = [\"w\"])\n\ndef main(item):\n    gather(spawn(items = [\"21\", \"22\", \"23\", \"24\", \"25\"], entry = \"worker\"))\n"
	engine := h.engine(t)
	file := writeFlow(t, flow)
	done := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		done <- err
	}()

	require.True(t, receivedWithin(full), "two children never ran at once")

	// Two children hold the two slots. The other three wait in the queue until a slot is free: a status read shows it, with no wait on time.
	// Spawn may not have enqueued every child when the first two start, so the count is polled.
	items := []string{"21", "22", "23", "24", "25"}

	assert.True(t, waitUntil(func() bool { return countEnqueued(engine, items) == 3 }), "three children wait while two run")

	close(release)
	require.NoError(t, <-done)

	assert.Equal(t, int32(2), peak.Load(), "the items queue holds agents.max_concurrent runs at once")
}

func TestSpawnGather_CancelOfTheParentWhileItGathersEndsTheChildren(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Item == testItem {
			return nil
		}

		<-ctx.Done()

		return ctx.Err()
	}

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan error, 1)
	file := writeFlow(t, swarmFile(""))

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		done <- err
	}()

	// Cancel ends the parent at its next step, so a spawn still creating children would leave the last one without a run.
	// Wait until every child has a run, and child 22 still runs, so the cancel lands while the parent gathers.
	eventually(t, func() bool {
		for _, item := range []string{"21", "23"} {
			if _, found, err := latestRun(engine.ctx, item); err != nil || !found {
				return false
			}
		}

		last, found, err := latestRun(engine.ctx, "22")

		return err == nil && found && last.Status.Status == dbos.WorkflowStatusPending
	})
	require.NoError(t, client.Cancel(testItem))

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(eventTimeout):
		t.Fatal("the parent did not end after the cancel")
	}

	for _, item := range []string{"21", "22", "23"} {
		eventually(t, func() bool { return childStatus(t, engine, item) == dbos.WorkflowStatusCancelled })
	}
}

// agentSwarm is a parent whose child runs an agent step, so a child that fails has a failed phase.
const agentSwarm = `def worker(item):
    agent(name = "work", prompt = "prompts/work.md", model = "m")

def main(item):
    gather(spawn(items = ["21"], entry = "worker"), fail = "raise")
`

// claimFails is a lifecycle that cannot claim one item.
type claimFails struct {
	*fakeLifecycle
	item string
}

func (c claimFails) Claim(ctx context.Context, project, item string) (string, error) {
	if item == c.item {
		return "", errFake
	}

	return c.fakeLifecycle.Claim(ctx, project, item)
}

// failedAgentSwarm runs agentSwarm to its failure and returns the engine.
func failedAgentSwarm(t *testing.T, h *harness) *Engine {
	t.Helper()

	h.agents.err = errFake
	h.flowIs(t, agentSwarm)

	engine := h.engine(t)
	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	return engine
}

func TestSpawnGather_AResumeThatCannotClaimTheFailedChildReportsItAndReopensNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := failedAgentSwarm(t, h)

	client, err := OpenClient(t.Context(), h.opts, ReleaseDeps{Lifecycle: claimFails{h.life, "21"}, Trees: h.trees, Clock: clock{Release: time.Second}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	require.ErrorIs(t, client.Resume(testItem), errFake)
	assert.Equal(t, dbos.WorkflowStatusError, childStatus(t, engine, "21"), "the child stays failed")
}

func TestSpawnGather_AResumeThatCannotReopenTheFailedChildGivesItsFreshClaimBack(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := failedAgentSwarm(t, h)
	client := h.client(t, testVersion)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, "item-21-r1")
	require.NoError(t, err)

	var phases []string

	for _, step := range steps {
		if step.ChildWorkflowID != "" {
			phases = append(phases, step.ChildWorkflowID)
		}
	}

	require.NotEmpty(t, phases)
	require.NoError(t, dbos.DeleteWorkflows(engine.ctx, phases))

	require.ErrorIs(t, client.Resume(testItem), errNoRow)
	assert.Contains(t, h.life.releasedBy(), "owner-21#2", "the fresh claim of the child is given back")
}

func TestSpawnGather_AChildWhoseInputCannotBeReadTakesNoClaim(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)

	_, _, _, err := client.childClaim(dbos.WorkflowStatus{ID: "item-21-r1", Name: itemName, Input: "not json"})

	require.ErrorContains(t, err, "read the input of run item-21-r1")
}

func TestSpawnGather_ACallWithNoArgumentsNamesTheMissingOne(t *testing.T) {
	t.Parallel()

	for call, want := range map[string]string{"spawn(*[])": "spawn: missing argument for items", "gather(*[])": "gather: missing argument for runs"} {
		_, err := newHarness(t).engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(call)))

		require.ErrorContains(t, err, want)
	}
}

func TestSpawnGather_TheItemsQueueReportsAStoreThatIsClosed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	require.ErrorContains(t, engine.registerItemQueue(2), "register the items queue")
}

// pickFails picks a login for every item but one.
type pickFails struct{ item string }

func (p pickFails) Pick(_ context.Context, _, item string, _ []string, _ bool) (string, error) {
	if item == p.item {
		return "", errFake
	}

	return testLogin, nil
}

func TestSpawnGather_SpawnReportsAnItemWhoseLoginCannotBePicked(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.picker = pickFails{item: "22"}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, swarmFile("")))

	require.ErrorContains(t, err, "pick the login of item 22")
	assert.Empty(t, h.commands.ranBy("21"), "no child started")
}

func TestSpawnGather_SpawnReportsAChildTheQueueCannotStart(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	engine.itemQueue = nil

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, swarmFile("")))

	require.ErrorContains(t, err, "start the run of item 21")
}

func TestSpawnGather_TheReadsOfAStoppedEngineReportTheError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	require.NoError(t, engine.Shutdown())

	_, err := engine.planOne(t.Context(), ItemInput{Item: testItem, Project: testProject}, "21", nil)
	require.Error(t, err, "the plan reads the latest run")

	_, err = engine.readRuns([]spawnedRun{{Item: "21", Run: "item-21-r1"}})
	require.ErrorContains(t, err, "read the runs of the spawned items")

	_, err = engine.gather(t.Context(), ItemInput{Item: testItem, Project: testProject}, []spawnedRun{{Item: "21", Run: "item-21-r1"}}, false, false)
	require.ErrorContains(t, err, "read the runs of the spawned items")
}

func TestSpawnGather_AnOpenRunWhoseInputCannotBeReadIsReportedNotGuessed(t *testing.T) {
	t.Parallel()

	err := openRunError(run{Status: dbos.WorkflowStatus{ID: "item-21-r1", Input: "not json"}}, "21", testProject)

	require.ErrorContains(t, err, "read the input of run item-21-r1")
}

func TestSpawnGather_AResultThatCannotBeReadIsAFailedOutcome(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	out := engine.outcome(spawnedRun{Item: "21", Run: "item-21-r1"}, dbos.WorkflowStatus{ID: "item-21-r1", Status: dbos.WorkflowStatusSuccess, Output: "not json"}, true)

	assert.False(t, out.OK)
	assert.Contains(t, out.Error, "read the result of item-21-r1")
}

func TestSpawnGather_AWaitEndsWhenItsContextEnds(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	startOpenRun(t, h, engine, "21", testProject)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := engine.gather(ctx, ItemInput{Item: testItem, Project: testProject}, []spawnedRun{{Item: "21", Run: "item-21-r1"}}, false, false)

	require.ErrorIs(t, err, context.Canceled)
}

func TestSpawnGather_AFailedCancelOfAnOpenChildIsReportedWithItsRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Item == "22" {
			return errFake
		}

		return nil
	}

	engine := h.engine(t)

	startOpenRun(t, h, engine, "21", testProject)

	_, err := engine.RunFile("22", testProject, writeFlow(t, bodyFlow(`command(name = "w", run = ["w"])`)))
	require.Error(t, err)

	// A file where the folder of the cancel markers belongs makes every marker fail.
	require.NoError(t, os.WriteFile(filepath.Join(engine.dir, cancelsDir), nil, 0o600))

	refs := []spawnedRun{{Item: "21", Run: "item-21-r1"}, {Item: "22", Run: "item-22-r1"}}
	_, err = engine.gather(t.Context(), ItemInput{Item: testItem, Project: testProject}, refs, true, false)

	require.ErrorContains(t, err, "mark the cancel of run item-21-r1")
	require.ErrorContains(t, err, "cancel run item-21-r1")
}
