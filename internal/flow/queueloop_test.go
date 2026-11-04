package flow

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/preflight"
)

// loopPreflight answers the preflight of each item. An item that is not in stops has no pull request that stops it.
type loopPreflight struct {
	mu    sync.Mutex
	stops map[string]preflight.Result
	asked []string
}

func (p *loopPreflight) Preflight(_ context.Context, _, item string, _ preflight.Request, _ string) (preflight.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.asked = append(p.asked, item)

	return p.stops[item], nil
}

func (p *loopPreflight) askedItems() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.asked)
}

func TestQueueLoop_AnItemWithAnOpenPullRequestOfAnotherLoginStartsNoAgentAndIsHeld(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP0), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP2))
	r.h.preflight = &loopPreflight{stops: map[string]preflight.Result{"22": {Stop: preflight.Claimed, Lines: []string{"STOP: claimed by another login"}}}}

	require.NoError(t, r.run(t))
	assert.ElementsMatch(t, []string{"21", "23"}, r.agents.started())
	assert.Zero(t, r.agents.callsOf("22"), "an item that another login holds starts no agent")
	assert.Equal(t, []string{testProject + "|22|hold"}, r.w.statuses)
	require.Len(t, r.w.notes, 1)
	assert.Contains(t, r.w.notes[0], "claimed by another login")
}

func TestQueueLoop_APullRequestOpenedSinceTheLastPassStopsTheItemThatThePassReads(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP1))
	pf := &loopPreflight{stops: map[string]preflight.Result{"30": {Stop: preflight.Claimed, Lines: []string{"STOP: claimed by another login"}}}}
	r.h.preflight = pf
	r.agents.setAct(func(_ context.Context, item string) error {
		if item == "21" {
			// Another login opens a pull request for 30 while 21 runs. The next pass reads 30 as ready.
			r.q.mu.Lock()
			r.q.items = append(r.q.items, queueItem("30", model.SeverityP1))
			r.q.mu.Unlock()
		}

		return nil
	})

	require.NoError(t, r.run(t))
	assert.Equal(t, []string{"21"}, r.agents.started())
	assert.Contains(t, pf.askedItems(), "30", "the child of the new pass reads the forge before its agent")
	assert.Zero(t, r.agents.callsOf("30"))
}

// loopAgents is an agent runner whose behavior is set for each item. The default is an agent that ends at once.
// start lists the items in the order of their first call, so a test sees which items began and when.
type loopAgents struct {
	mu    sync.Mutex
	start []string
	calls map[string]int
	act   func(ctx context.Context, item string) error
}

func (a *loopAgents) Run(ctx context.Context, request AgentRequest) (agent.Result, error) {
	a.mu.Lock()
	if a.calls == nil {
		a.calls = map[string]int{}
	}

	if a.calls[request.Item] == 0 {
		a.start = append(a.start, request.Item)
	}

	a.calls[request.Item]++
	act := a.act
	a.mu.Unlock()

	if act != nil {
		if err := act(ctx, request.Item); err != nil {
			return agent.Result{Outcome: agent.OutcomeNotStarted, Note: err.Error()}, err
		}
	}

	return agent.Result{Outcome: agent.OutcomeExited}, nil
}

func (a *loopAgents) setAct(act func(ctx context.Context, item string) error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.act = act
}

func (a *loopAgents) started() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Clone(a.start)
}

func (a *loopAgents) callsOf(item string) int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.calls[item]
}

// loopRig runs examples/queue/workflow.star, copied with its prompts, over the fake collaborators. The parent is the test item.
type loopRig struct {
	h      *harness
	q      *fakeQueue
	w      *fakeWriter
	agents *loopAgents
	file   string
}

func newLoopRig(t *testing.T, items ...model.Item) *loopRig {
	t.Helper()

	folder := t.TempDir()
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", "queue"))))

	h := newHarness(t)
	h.preflight = &loopPreflight{}
	h.opts.Env = map[string]string{"BASE": "integration", "OWNER_LOGIN": "owner"}
	// One login runs one agent phase at a time, so each item has its own login. The slots of the items queue then decide how many items run at once.
	h.logins = fakeLogins{login: testLogin, byItem: map[string]string{"21": "login-21", "22": "login-22", "23": "login-23", "24": "login-24", "30": "login-30"}}

	q := &fakeQueue{items: items}
	w := newFakeWriter()
	h.queue = q
	h.writer = w

	return &loopRig{h: h, q: q, w: w, agents: &loopAgents{}, file: filepath.Join(folder, "workflow.star")}
}

// engine builds and launches an engine over the rig, with the agents of the rig in place of the fake of the harness.
func (r *loopRig) engine(t *testing.T) *Engine {
	t.Helper()

	deps := r.h.deps()
	deps.Agents = r.agents

	engine, err := NewEngine(t.Context(), r.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	return engine
}

// crashEngine is an engine that the returned function stops, as a crash would. The test shuts the engine down after the stop.
func (r *loopRig) crashEngine(t *testing.T) (*Engine, func()) {
	t.Helper()

	deps := r.h.deps()
	deps.Agents = r.agents

	stopCtx, stop := context.WithCancel(t.Context())
	engine, err := NewEngine(stopCtx, r.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	return engine, stop
}

func (r *loopRig) run(t *testing.T) error {
	t.Helper()

	engine := r.engine(t)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	_, err := engine.RunFile(testItem, testProject, r.file)

	return err
}

func TestQueueLoop_ReadyItemsStartInFreeSlotsWhileOneItemLoops(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP0), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP2), queueItem("24", model.SeverityP2))
	loops := make(chan struct{})

	r.agents.setAct(func(ctx context.Context, item string) error {
		if item != "21" {
			return nil
		}

		select {
		case <-loops:
		case <-ctx.Done():
		}

		return nil
	})

	engine := r.engine(t)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	done := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, r.file)
		done <- err
	}()

	require.True(t, waitUntil(func() bool { return itemEnded(engine, "22") && itemEnded(engine, "23") && itemEnded(engine, "24") }),
		"22, 23 and 24 pass through the one free slot while 21 still loops")
	assert.False(t, itemEnded(engine, "21"), "the item that loops keeps its run open")

	close(loops)
	require.NoError(t, <-done)
	assert.ElementsMatch(t, []string{"21", "22", "23", "24"}, r.agents.started())
}

func TestQueueLoop_AMergeMakesANewItemReadyAndTheNextPassStartsIt(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP1))
	r.agents.setAct(func(_ context.Context, item string) error {
		if item == "21" {
			// The merge of 21 unblocks 30: the tracker lists it after the merge.
			r.q.mu.Lock()
			r.q.items = append(r.q.items, queueItem("30", model.SeverityP1))
			r.q.mu.Unlock()
		}

		return nil
	})

	require.NoError(t, r.run(t))
	assert.Equal(t, []string{"21", "30"}, r.agents.started())
}

func TestQueueLoop_TheRunEndsWhenNoWorkRemains(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP1), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP1))

	require.NoError(t, r.run(t))
	assert.ElementsMatch(t, []string{"21", "22", "23"}, r.agents.started())

	for _, item := range []string{"21", "22", "23"} {
		assert.Equal(t, 1, r.agents.callsOf(item), "each item ran once")
	}

	assert.Empty(t, r.w.statuses, "a run with no failure holds nothing")

	// The parent reads the queue once in each pass, and each child reads the hold of its item once. Three runs need at most four passes, so ten reads is a wide margin.
	// A loop that keeps ended runs never sees an empty list, and it reads the queue until its bound of 500 passes.
	r.q.mu.Lock()
	reads := r.q.reads
	r.q.mu.Unlock()

	assert.LessOrEqual(t, reads, 10, "the loop stops reading the queue once every run has ended")
}

func TestQueueLoop_OnePassStartsNoMoreRunsThanFreeSlots(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP0), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP2), queueItem("24", model.SeverityP2))
	loops := make(chan struct{})

	r.agents.setAct(func(ctx context.Context, _ string) error {
		select {
		case <-loops:
		case <-ctx.Done():
		}

		return nil
	})

	engine := r.engine(t)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	done := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, r.file)
		done <- err
	}()

	slots := r.h.opts.MaxConcurrent
	require.Less(t, slots, 4, "the rig has more ready items than slots")
	require.True(t, waitUntil(func() bool { return len(r.agents.started()) == slots }), "the free slots fill")

	// A run that the loop spawned beyond the free slots exists at once, queued behind the others.
	spawned := 0

	for _, item := range []string{"21", "22", "23", "24"} {
		if _, found, err := latestRun(engine.ctx, item); err == nil && found {
			spawned++
		}
	}

	assert.Equal(t, slots, spawned, "the first pass spawns one run for each free slot and no more")

	close(loops)
	require.NoError(t, <-done)
}

func TestQueueLoop_ACrashAfterSpawnAndAResumeStartNoItemTwice(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP1), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP1))
	r.agents.setAct(func(ctx context.Context, item string) error {
		if item == "21" || item == "22" {
			<-ctx.Done()

			return ctx.Err()
		}

		return nil
	})

	first, stop := r.crashEngine(t)

	go func() { _, _ = first.RunFile(testItem, testProject, r.file) }() //nolint:errcheck // the crash ends the run with an error

	require.True(t, waitUntil(func() bool { return len(r.agents.started()) == 2 }), "both slots are in use when the engine stops")
	stop()
	require.NoError(t, first.Shutdown())

	r.agents.setAct(nil)

	second := r.engine(t)
	t.Cleanup(func() { require.NoError(t, second.Shutdown()) })

	_, err := second.RunFile(testItem, testProject, r.file)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"21", "22", "23"}, r.agents.started(), "every item started, and none has a second entry")

	for _, item := range []string{"21", "22", "23"} {
		last, found, lookErr := latestRun(second.ctx, item)
		require.NoError(t, lookErr)
		require.True(t, found)
		assert.Equal(t, 1, last.Attempt, "item %s kept its first attempt", item)
	}
}

func TestQueueLoop_AHeldItemNeverStarts(t *testing.T) {
	t.Parallel()

	held := queueItem("22", model.SeverityP0)
	held.Hold = true

	r := newLoopRig(t, queueItem("21", model.SeverityP1), held)

	require.NoError(t, r.run(t))
	assert.Equal(t, []string{"21"}, r.agents.started())
	assert.Zero(t, r.agents.callsOf("22"))
}

func TestQueueLoop_AFailedChildIsHeldAndTheLoopGoesOn(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP0), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP2))
	r.agents.setAct(func(_ context.Context, item string) error {
		if item == "21" {
			return errFake
		}

		return nil
	})

	require.NoError(t, r.run(t))
	assert.ElementsMatch(t, []string{"21", "22", "23"}, r.agents.started(), "the items after the failure still run")
	assert.Equal(t, []string{testProject + "|21|hold"}, r.w.statuses, "only the failed item is held")
	require.Len(t, r.w.notes, 1)
	assert.Contains(t, r.w.notes[0], "|21|")
	assert.Contains(t, r.w.notes[0], "The run of this item failed")
	assert.Equal(t, 1, r.agents.callsOf("21"), "a failed item does not start again")
}
