package flow

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// chainFlow is a chain of spawns: the entry lN spawns the item 2N with the entry l<N+1>, and l4 runs the command leaf. main spawns item 21 with the entry top.
// Each level runs the command at before it spawns. The entry l2 makes the items 21, 23 and 24 run at the depths 1, 2 and 3. The entry l1 adds depth 4.
func chainFlow(top string) string {
	return `def down(n):
    command(name = "at", run = ["at"])
    r = gather(spawn(items = ["2%d" % n], entry = "l%d" % n)).results[0]
    if not r.ok:
        fail(r.error)

def l1(item):
    down(2)

def l2(item):
    down(3)

def l3(item):
    down(4)

def l4(item):
    command(name = "leaf", run = ["leaf"])

def main(item):
    r = gather(spawn(items = ["21"], entry = "` + top + `")).results[0]
    if not r.ok:
        fail(r.error)
    command(name = "after", run = ["after"])
`
}

// inputOf reads the input of the last run of an item.
func inputOf(t *testing.T, engine *Engine, item string) (ItemInput, dbos.WorkflowStatus) {
	t.Helper()

	last, found, err := latestRun(engine.ctx, item)
	require.NoError(t, err)
	require.True(t, found, "item %s has a run", item)

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterWorkflowIDs(last.Status.ID), dbos.WithFilterLoadInput(true))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	in, err := run{Status: rows[0]}.input()
	require.NoError(t, err)

	return in, rows[0]
}

func TestNestedSpawn_AChainOfSpawnsRunsToDepthThreeWithOneSlotForEachLevel(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.MaxConcurrent, h.opts.MaxReviewConcurrent = 1, 1
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, chainFlow("l2")), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"leaf"}, h.commands.ranBy("24"), "the run at depth 3 ran once")
	assert.Equal(t, []string{"after"}, h.commands.ranBy(testItem), "the root saw its child ok")
}

func TestNestedSpawn_EachRunRecordsItsDepthAndItsParentAndRunsOnTheQueueOfItsDepth(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, chainFlow("l2")), modeOff())
	require.NoError(t, err)

	root, _, err := latestRun(engine.ctx, testItem)
	require.NoError(t, err)

	parent := root.Status.ID

	for depth, item := range map[int]string{1: "21", 2: "23", 3: "24"} {
		in, row := inputOf(t, engine, item)
		assert.Equal(t, depth, in.Depth, "the depth of item %s", item)
		assert.Equal(t, itemsQueueOf(depth), row.QueueName, "the queue of item %s", item)
		assert.NotEmpty(t, in.Parent, "item %s names its parent", item)
	}

	first, _ := inputOf(t, engine, "21")
	assert.Equal(t, parent, first.Parent, "the child of the root names the root")

	third, _ := inputOf(t, engine, "24")
	second, _, err := latestRun(engine.ctx, "23")
	require.NoError(t, err)
	assert.Equal(t, second.Status.ID, third.Parent, "the run at depth 3 names the run at depth 2")
	assert.Equal(t, []string{"items", "items.2", "items.3"}, []string{itemsQueueOf(1), itemsQueueOf(2), itemsQueueOf(3)})
}

func TestNestedSpawn_ASpawnPastTheCapNamesTheDepthAndTheCap(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		top  string
		cap  int
		want string
	}{
		"default cap of 3": {top: "l1", want: "item 24 would run at depth 4, and the project has a cap of 3. Raise max_spawn_depth in the project file, or spawn fewer levels"},
		"project cap of 1": {top: "l2", cap: 1, want: "item 23 would run at depth 2, and the project has a cap of 1."},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			if c.cap > 0 {
				h.facts = func(string) model.Project { return model.Project{MaxSpawnDepth: c.cap} }
			}

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, chainFlow(c.top)), modeOff())

			require.ErrorContains(t, err, c.want)
			assert.Empty(t, h.commands.ranBy("24"), "no run past the cap")
		})
	}
}

func TestNestedSpawn_AnEngineWithTwoLevelsCapsAProjectThatSetsThree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.MaxSpawnDepth = 2

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, chainFlow("l2")), modeOff())

	require.ErrorContains(t, err, "item 24 would run at depth 3, and the project has a cap of 2.")
}

func TestNestedSpawn_AStartOutsideARunReportsTheRunWithNoID(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	s := &script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}

	require.ErrorContains(t, s.startRuns(nil, defaultEntry, ""), "read the id of the run of item "+testItem)
}

func TestNestedSpawn_ResumeOnThisBuildReachesTheRunsAtDepthTwoAndThree(t *testing.T) {
	t.Parallel()

	for depth, item := range map[int]string{2: "23", 3: "24"} {
		t.Run(itemsQueueOf(depth), func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			failAt(t, h, chainFlow("l2"), item)

			require.NoError(t, h.client(t, otherBuild).ResumeOnThisBuild(testItem))

			h.opts.Version = otherBuild
			done := runInBackground(h.engine(t), testItem)

			require.NoError(t, receive(t, done).err)
			assert.Contains(t, h.commands.ranBy(testItem), "after", "the root ends on the new build")
		})
	}
}

// raiseBelow is a root that spawns item 20, and item 20 gathers the items 21, 22 and 23 at depth 2 with fail = "raise".
const raiseBelow = `def worker(item):
    command(name = "w", run = ["w"])

def mid(item):
    gather(spawn(items = ["21", "22", "23"], entry = "worker"), fail = "raise")

def main(item):
    r = gather(spawn(items = ["20"], entry = "mid")).results[0]
    if not r.ok:
        fail(r.error)
    command(name = "after", run = ["after"])
`

// raiseAtDepthTwo runs raiseBelow until item 22 fails while 21 or 23 works, so the raise cancels runs at depth 2. It shuts the engine down and heals the items.
func raiseAtDepthTwo(t *testing.T, h *harness) {
	t.Helper()

	h.opts.MaxConcurrent, h.opts.MaxReviewConcurrent = 3, 3
	h.flowIs(t, raiseBelow)

	var healed, once atomic.Bool

	started := make(chan struct{}) // closed when a sibling of the failed run works

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		switch {
		case healed.Load():
			return nil
		case request.Item == "22":
			receive(t, started)

			return errFake
		case request.Item == "21" || request.Item == "23":
			if once.CompareAndSwap(false, true) {
				close(started)
			}

			receive(t, ctx.Done())

			return ctx.Err()
		}

		return nil
	}

	engine := h.engine(t)
	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)
	require.NoError(t, engine.Shutdown())
	healed.Store(true)
}

func TestNestedSpawn_ResumeOnThisBuildPutsTheRunsAtDepthTwoThatARaiseCanceledBackOnTheirQueue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	raiseAtDepthTwo(t, h)

	require.NoError(t, h.client(t, otherBuild).ResumeOnThisBuild(testItem))

	h.opts.Version = otherBuild
	done := runInBackground(h.engine(t), testItem)

	require.NoError(t, receive(t, done).err)
	assert.Contains(t, h.commands.ranBy(testItem), "after", "the root ends on the new build")
}

func TestNestedSpawn_AResumeOfTheRootPutsTheRunsAtDepthTwoThatARaiseCanceledBackOnTheirQueue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	raiseAtDepthTwo(t, h)

	require.NoError(t, h.client(t, testVersion).Resume(testItem))

	done := runInBackground(h.engine(t), testItem)

	require.NoError(t, receive(t, done).err)
	assert.Contains(t, h.commands.ranBy(testItem), "after", "the root ends")
}

func TestNestedSpawn_ADryRunWithASpawnDepthOfOneRefusesTheRunAtDepthTwo(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), dryFile(t, chainFlow("l2")), "12", map[string]string{}, &out, WithSpawnDepth(1))

	require.ErrorContains(t, err, "item 23 would run at depth 2, and the project has a cap of 1.")
}

func TestNestedSpawn_ARootThatBeehaveRunQueuesWaitsForItsChainWithOneSlotForEachLevel(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.MaxConcurrent, h.opts.MaxReviewConcurrent = 1, 1
	engine := h.engine(t)
	client := h.client(t, testVersion)

	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()

	_, err := client.JoinRun(ctx, testItem, testProject, writeFlow(t, chainFlow("l2")), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"leaf"}, h.commands.ranBy("24"), "the run at depth 3 ran once")

	_, root := inputOf(t, engine, testItem)
	assert.Equal(t, rootsQueueName, root.QueueName, "the root has a queue of its own")
}

// cancelBelow runs the chain to depth 3, waits until item 24 works, cancels one item and returns the engine.
func cancelBelow(t *testing.T, target string) *Engine {
	t.Helper()

	h := newHarness(t)
	working := make(chan struct{}) // closed when the run at depth 3 works

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Item != "24" {
			return nil
		}

		close(working)
		receive(t, ctx.Done())

		return ctx.Err()
	}

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan error, 1)
	file := writeFlow(t, chainFlow("l2"))

	go func() {
		_, err := engine.RunFile(testItem, testProject, file, modeOff())
		done <- err
	}()

	receive(t, working)
	require.NoError(t, client.Cancel(target))
	require.Error(t, receive(t, done))

	return engine
}

func TestNestedSpawn_ACancelOfTheRootCancelsTheRunsAtDepthTwoAndThree(t *testing.T) {
	t.Parallel()

	engine := cancelBelow(t, testItem)

	for _, item := range []string{"21", "23", "24"} {
		waitOn(t, condition(func() bool { return childStatus(t, engine, item) == dbos.WorkflowStatusCancelled }))
	}
}

func TestNestedSpawn_ACancelOfAMiddleRunCancelsTheRunsBelowItAndNothingAbove(t *testing.T) {
	t.Parallel()

	engine := cancelBelow(t, "23")

	waitOn(t, condition(func() bool { return childStatus(t, engine, "24") == dbos.WorkflowStatusCancelled }))
	waitOn(t, condition(func() bool { return childStatus(t, engine, "21") == dbos.WorkflowStatusError }))

	_, root := inputOf(t, engine, testItem)
	assert.Equal(t, dbos.WorkflowStatusError, root.Status, "the root fails and is not canceled")
}

// failAt runs flow with every command of the item fails failing, shuts the engine down and heals the item.
func failAt(t *testing.T, h *harness, flow, fails string) {
	t.Helper()

	var healed atomic.Bool

	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Item == fails && !healed.Load() {
			return errFake
		}

		return nil
	}

	h.flowIs(t, flow)
	engine := h.engine(t)
	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)
	require.NoError(t, engine.Shutdown())
	healed.Store(true)
}

func TestNestedSpawn_AnEngineThatStartsWithALowerCapStillRunsARunThatWaitsAtDepthThree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	failAt(t, h, chainFlow("l2"), "24")

	require.NoError(t, h.client(t, testVersion).Resume(testItem))

	h.opts.MaxSpawnDepth = 1
	done := runInBackground(h.engine(t), testItem)

	require.NoError(t, receive(t, done).err)
	assert.Contains(t, h.commands.ranBy(testItem), "after", "the root ends")
}

// failAfterSpawn is a chain where item 23, at depth 2, runs the command post after its child 24 at depth 3 ended.
const failAfterSpawn = `def l4(item):
    command(name = "leaf", run = ["leaf"])

def l3(item):
    gather(spawn(items = ["24"], entry = "l4"))
    command(name = "post", run = ["post"])

def l2(item):
    r = gather(spawn(items = ["23"], entry = "l3")).results[0]
    if not r.ok:
        fail(r.error)

def main(item):
    r = gather(spawn(items = ["21"], entry = "l2")).results[0]
    if not r.ok:
        fail(r.error)
    command(name = "after", run = ["after"])
`

func TestNestedSpawn_AnEngineThatStartsWithALowerCapReplaysTheSpawnOfARunAtDepthTwo(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	failAt(t, h, failAfterSpawn, "23")

	require.NoError(t, h.client(t, testVersion).Resume(testItem))

	h.opts.MaxSpawnDepth = 1
	done := runInBackground(h.engine(t), testItem)

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"leaf"}, h.commands.ranBy("24"), "the run at depth 3 ran once")
}

// queueOldRoot queues the run of testItem on the items queue, as a build before the roots queue did.
func queueOldRoot(t *testing.T, client *Client) {
	t.Helper()

	in, err := pinnedInput(testItem, testProject, writeFlow(t, chainFlow("l2")))
	require.NoError(t, err)

	_, err = dbos.Enqueue[ItemResult](client.flow, itemsQueueName, itemName, in, dbos.WithEnqueueWorkflowID(runID(testItem, 1)), dbos.WithEnqueueApplicationVersion(client.version))
	require.NoError(t, err)
}

func TestNestedSpawn_ARootThatAnOlderBuildQueuedOnItemsRunsAndResumesOnThisBuild(t *testing.T) {
	t.Parallel()

	t.Run("queued while no engine ran", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		require.NoError(t, h.engine(t).Shutdown())

		client := h.client(t, testVersion)
		queueOldRoot(t, client)

		engine := h.engine(t)
		_, err := client.JoinRun(t.Context(), testItem, testProject, writeFlow(t, chainFlow("l2")), modeOff())

		require.NoError(t, err)

		_, root := inputOf(t, engine, testItem)
		assert.Equal(t, itemsQueueName, root.QueueName, "the root keeps the queue it was queued on")
		assert.Equal(t, []string{"leaf"}, h.commands.ranBy("24"))
	})

	t.Run("failed, then resumed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		var healed atomic.Bool

		h.commands.before = func(_ context.Context, request CommandRequest) error {
			if request.Item == "23" && !healed.Load() {
				return errFake
			}

			return nil
		}

		engine := h.engine(t)
		client := h.client(t, testVersion)
		queueOldRoot(t, client)
		waitOn(t, condition(func() bool { return childStatus(t, engine, testItem) == dbos.WorkflowStatusError }))
		require.NoError(t, engine.Shutdown())
		healed.Store(true)

		require.NoError(t, client.Resume(testItem))

		h.engine(t)
		_, err := client.JoinRun(t.Context(), testItem, testProject, writeFlow(t, chainFlow("l2")), modeOff())

		require.NoError(t, err)
		assert.Contains(t, h.commands.ranBy(testItem), "after", "the root ends")
	})
}
