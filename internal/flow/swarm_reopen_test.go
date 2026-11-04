package flow

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatherOutput returns the recorded output of the latest gather step of the parent, and false while the parent has none.
func gatherOutput(engine *Engine) (string, bool) {
	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	if err != nil {
		return "", false
	}

	for _, s := range steps {
		if text, isText := s.Output.(string); isText && s.StepName == "gather:fan#1" {
			return text, true
		}
	}

	return "", false
}

// handCancel is `beehave cancel` for an item, as a user types it.
func handCancel(client *Client, item string) error {
	return client.Cancel(item)
}

// afterFails is a parent that gathers three children and then runs a command that the test makes fail. fail="return" never cancels a child.
const afterFails = `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    done = gather(spawn(items = ["21", "22", "23"], entry = "worker", name = "fan"), name = "fan")
    command(name = "after", run = ["after"] + [r.item + ":" + ("ok" if r.ok else r.error) for r in done.results])
`

func TestSpawnGather_AResumeKeepsAChildThatTheUserCanceledByHand(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var (
		failing atomic.Bool
		once    sync.Once
	)

	failing.Store(true)

	held := make(chan struct{})

	// Item 23 stays open until the user cancels it by hand, from a goroutine of the test. The parent then fails in its own command, not in a child.
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		switch {
		case request.Item == "23" && failing.Load():
			once.Do(func() { close(held) })
			<-ctx.Done()

			return ctx.Err()
		case request.Item == testItem && failing.Load():
			return errFake
		}

		return nil
	}

	engine := h.engine(t)
	resumer := h.client(t, testVersion)

	canceled := make(chan error, 1)

	go func() {
		<-held
		canceled <- handCancel(resumer, "23")
	}()

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, afterFails))
	require.ErrorIs(t, err, errFake)
	require.NoError(t, <-canceled)
	require.Equal(t, dbos.WorkflowStatusCancelled, childStatus(t, engine, "23"))

	failing.Store(false)
	require.NoError(t, resumer.Resume(testItem))

	if !waitUntil(func() bool { return childStatus(t, engine, testItem) == dbos.WorkflowStatusSuccess }) {
		t.Fatal(dumpRuns(engine, testItem, "21", "22", "23"))
	}

	assert.Equal(t, dbos.WorkflowStatusCancelled, childStatus(t, engine, "23"), "the child that the user canceled stays canceled")
	assert.Empty(t, h.commands.ranBy("23"), "the canceled child did not run again")

	h.life.mu.Lock()
	assert.Equal(t, 1, h.life.takes["23"], "no fresh claim for a child that stays canceled")
	h.life.mu.Unlock()
}

func TestSpawnGather_TheGatherRecordsTheRunsThatTheRaiseCanceled(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	started := make(chan struct{})

	var once atomic.Bool

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Item == "22" {
			<-started

			return errFake
		}

		if once.CompareAndSwap(false, true) {
			close(started)
		}

		<-ctx.Done()

		return ctx.Err()
	}

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, swarmFile(`fail = "raise", `)))

	require.ErrorContains(t, err, "item 22 did not finish")

	var recorded gathered

	text, found := gatherOutput(engine)
	require.True(t, found)
	require.NoError(t, json.Unmarshal([]byte(text), &recorded))
	assert.ElementsMatch(t, []string{"item-21-r1", "item-23-r1"}, recorded.Canceled, "the ids of the runs that the raise canceled, not the run that failed")
}

// mixedAgentSwarm is a parent whose item 21 fails and whose item 22 runs an agent that does not end on its own.
const mixedAgentSwarm = `def failer(item):
    command(name = "w", run = ["w"])

def sleeper(item):
    agent(name = "work", prompt = "prompts/work.md", model = "m")

def main(item):
    a = spawn(items = ["21"], entry = "failer", name = "a")
    b = spawn(items = ["22"], entry = "sleeper", name = "b")
    gather(a.runs + b.runs, fail = "raise", name = "fan")
`

func TestSpawnGather_AFailRaiseLeavesNoPhaseWaitingOrRunningAndAResumeRunsThemAgain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.agents.gate = make(chan struct{})
	h.flowIs(t, mixedAgentSwarm)

	var failing atomic.Bool

	failing.Store(true)

	h.commands.before = func(ctx context.Context, _ CommandRequest) error {
		if !failing.Load() {
			return nil
		}

		for h.agents.running() == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}

		return errFake
	}

	engine := h.engine(t)
	done := runInBackground(engine, testItem)

	select {
	case outcome := <-done:
		require.ErrorContains(t, outcome.err, "item 21 did not finish")
	case <-time.After(eventTimeout):
		t.Fatal(dumpRuns(engine, testItem, "21", "22"), "agents in flight:", h.agents.running())
	}

	require.Equal(t, dbos.WorkflowStatusCancelled, childStatus(t, engine, "22"))
	assert.Positive(t, phaseCount(t, engine), "the child did start a phase")
	assert.Zero(t, phaseCount(t, engine, dbos.WorkflowStatusEnqueued, dbos.WorkflowStatusPending), "the raise canceled the phase of the canceled child")

	// A resume reopens the canceled item and the canceled phase under it, so the item finishes.
	failing.Store(false)
	close(h.agents.gate)

	require.NoError(t, h.client(t, testVersion).Resume(testItem))

	if !waitUntil(func() bool { return itemEnded(engine, "21") && itemEnded(engine, "22") }) {
		t.Fatal(dumpRuns(engine, testItem, "21", "22"))
	}
}

// claimSometimes is a lifecycle that cannot claim one item while off is set.
type claimSometimes struct {
	*fakeLifecycle
	item string
	off  *atomic.Bool
}

func (c claimSometimes) Claim(ctx context.Context, project, item string) (string, error) {
	if item == c.item && c.off.Load() {
		return "", errFake
	}

	return c.fakeLifecycle.Claim(ctx, project, item)
}

func TestSpawnGather_AResumeThatFailsOnTheSecondChildLeavesTheParentAndLetsTheFirstRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var failing atomic.Bool

	failing.Store(true)

	held := make(chan struct{})

	var once sync.Once

	// Item 22 stays open until the raise cancels it. Item 21 fails once 22 runs.
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		switch {
		case !failing.Load():
			return nil
		case request.Item == "22":
			once.Do(func() { close(held) })
			<-ctx.Done()

			return ctx.Err()
		case request.Item == "21":
			<-held

			return errFake
		}

		return nil
	}

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, swarmFile(`fail = "raise", `)))
	require.ErrorContains(t, err, "item 21 did not finish")
	require.Equal(t, dbos.WorkflowStatusError, childStatus(t, engine, "21"))
	require.Equal(t, dbos.WorkflowStatusCancelled, childStatus(t, engine, "22"))

	var off atomic.Bool

	off.Store(true)

	deps := ReleaseDeps{Lifecycle: claimSometimes{h.life, "22", &off}, Trees: h.trees, Clock: clock{Release: time.Second}}
	client, err := OpenClient(t.Context(), h.opts, deps)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	failing.Store(false)

	// The claim of the second child fails after the first child was reopened with its fresh claim.
	require.ErrorIs(t, client.Resume(testItem), errFake)
	assert.Equal(t, dbos.WorkflowStatusError, childStatus(t, engine, testItem), "the parent is not rewound")
	assert.Equal(t, dbos.WorkflowStatusCancelled, childStatus(t, engine, "22"), "the child that did not reopen stays canceled")

	eventually(t, func() bool { return childStatus(t, engine, "21") == dbos.WorkflowStatusSuccess })
	eventually(t, func() bool { return slices.Contains(h.life.releasedBy(), "owner-21#2") })

	off.Store(false)
	require.NoError(t, client.Resume(testItem))

	if !waitUntil(func() bool { return childStatus(t, engine, testItem) == dbos.WorkflowStatusSuccess }) {
		t.Fatal(dumpRuns(engine, testItem, "21", "22", "23"))
	}

	assert.Equal(t, dbos.WorkflowStatusSuccess, childStatus(t, engine, "22"))
}

func TestSpawnGather_ARaiseOnAChildCanceledByHandSaysThatResumeKeepsItCanceled(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var once sync.Once

	held := make(chan struct{})

	// Item 23 stays open until the user cancels it by hand. Its cancel is the failure that makes the gather raise.
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Item == "23" {
			once.Do(func() { close(held) })
			<-ctx.Done()

			return ctx.Err()
		}

		return nil
	}

	engine := h.engine(t)
	resumer := h.client(t, testVersion)

	canceled := make(chan error, 1)

	go func() {
		<-held
		canceled <- handCancel(resumer, "23")
	}()

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, swarmFile(`fail = "raise", `)))
	require.NoError(t, <-canceled)
	require.ErrorContains(t, err, "item 23 did not finish")
	require.ErrorContains(t, err, "It was canceled by hand, and beehave resume 12 does not reopen a child canceled by hand")
}
