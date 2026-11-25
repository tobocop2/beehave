package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
)

// countSteps counts the recorded calls whose step name starts with prefix. A sub-step of a call has a slash and is not a call.
func countSteps(steps []dbos.StepInfo, prefix string) int {
	return len(slices.DeleteFunc(slices.Clone(steps), func(step dbos.StepInfo) bool {
		return !strings.HasPrefix(step.StepName, prefix) || strings.Contains(step.StepName, "/")
	}))
}

// holdsItem21 returns an agent action. Item 21 fails at once. Every other item stays open until the loop has held item 21, so the loop sees the failure of 21 alone, before any other run ends.
func holdsItem21(engine func() *Engine) func(ctx context.Context, item string) error {
	return func(ctx context.Context, item string) error {
		if item == "21" {
			return errFake
		}

		deadline := time.Now().Add(30 * time.Second)

		for time.Now().Before(deadline) && ctx.Err() == nil {
			steps, err := dbos.GetWorkflowSteps(engine().ctx, testRun)
			if err == nil && hasStep(steps, "item_status:hold-0-21#1") {
				return nil
			}

			time.Sleep(20 * time.Millisecond)
		}

		return errFake
	}
}

// loopPreflight answers the preflight of each item. An item that is not in pulls has no pull request that stops it.
type loopPreflight struct {
	noReads

	mu    sync.Mutex
	pulls map[string]model.PullFacts
	asked []string
}

func (p *loopPreflight) Preflight(_ context.Context, _, item string, _ preflight.Request) (preflight.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.asked = append(p.asked, item)

	return preflight.Result{}, nil
}

// ReadPull gives the pull request of the item, if the test set one.
func (p *loopPreflight) ReadPull(_ context.Context, _, item string) (model.PullFacts, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.pulls[item], nil
}

// otherLogin is a pull request that an open pull request of a login outside the own logins makes: it claims the item.
func otherLogin(number int) model.PullFacts {
	return model.PullFacts{Number: number, State: "OPEN", Author: "dev"}
}

func (p *loopPreflight) askedItems() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.asked)
}

func TestQueueLoop_AnItemWithAnOpenPullRequestOfAnotherLoginStartsNoAgentAndIsHeld(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP0), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP2))
	r.h.preflight = &loopPreflight{pulls: map[string]model.PullFacts{"22": otherLogin(5)}}

	require.NoError(t, r.run(t))
	assert.ElementsMatch(t, []string{"21", "23"}, r.agents.started())
	assert.Zero(t, r.agents.callsOf("22"), "an item that another login holds starts no agent")
	assert.Equal(t, []string{testProject + "|22|hold"}, r.w.statuses)
	require.Len(t, r.w.notes, 1)
	assert.Contains(t, r.w.notes[0], "its author is 'dev', who is not on the list of own logins. The item is claimed.")
}

func TestQueueLoop_TheEnginePushesAndOpensThePullRequestOnlyForAnAgentThatCommitted(t *testing.T) {
	t.Parallel()

	for name, commits := range map[string]bool{"commits": true, "commits_nothing": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := newLoopRig(t, queueItem("21", model.SeverityP1))
			// The item 21 reads its own id, title and body, which differ from the default fields of the fake reader, so the branch shows that it is the id of the item.
			r.h.items = &fakeItems{fields: map[string]string{"id": testItem, "title": "a title", "body": "a body"}, byItem: map[string]map[string]string{"21": {"id": "21", "title": "title of 21", "body": "body of 21"}}}
			r.agents.setAct(func(ctx context.Context, _ string) error {
				if !commits {
					return nil
				}

				commit := exec.CommandContext(ctx, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "work of 21")
				commit.Dir = r.h.trees.dir

				return commit.Run()
			})

			require.NoError(t, r.run(t))

			r.h.forge.mu.Lock()
			pushed, opened := slices.Clone(r.h.forge.pushed), slices.Clone(r.h.forge.opened)
			r.h.forge.mu.Unlock()

			if !commits {
				assert.Empty(t, pushed, "a step with no commit pushes nothing")
				assert.Empty(t, opened, "a step with no commit opens nothing")

				return
			}

			require.Len(t, pushed, 1)
			head := strings.TrimSpace(gitIn(t, r.h.trees.dir, "rev-parse", "HEAD"))
			assert.Equal(t, testProject+"|21|21|"+head+"|", pushed[0], "the engine pushes the commit of the step to the branch named by the id of the item, with no lease")
			require.Len(t, opened, 1)
			assert.Equal(t, testProject+"|21|title of 21|body of 21|integration|", opened[0], "the pull request of the item goes to BASE")
		})
	}
}

func TestQueueLoop_APullRequestOpenedSinceTheLastPassStopsTheItemThatThePassReads(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP1))
	pf := &loopPreflight{pulls: map[string]model.PullFacts{"30": otherLogin(6)}}
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
	h.opts.Env = map[string]string{"BASE": "integration", "OWNER_LOGIN": "owner", "PAUSE_AFTER_FAILURE": "50ms"}
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

	_, err := engine.RunFile(testItem, testProject, r.file, modeOff())

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
		_, err := engine.RunFile(testItem, testProject, r.file, modeOff())
		done <- err
	}()

	// 22, 23 and 24 pass through the one free slot while 21 still loops.
	waitOn(t, conditionOf(runOfEngine(t, engine), func() bool { return itemEnded(engine, "22") && itemEnded(engine, "23") && itemEnded(engine, "24") }))
	assert.False(t, itemEnded(engine, "21"), "the item that loops keeps its run open")

	close(loops)
	require.NoError(t, receive(t, done))
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

	engine := r.engine(t)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	_, err := engine.RunFile(testItem, testProject, r.file, modeOff())
	require.NoError(t, err)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)
	assert.Zero(t, countSteps(steps, "sleep:"), "a pass with no failure does not sleep")
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
		_, err := engine.RunFile(testItem, testProject, r.file, modeOff())
		done <- err
	}()

	slots := r.h.opts.MaxConcurrent
	require.Less(t, slots, 4, "the rig has more ready items than slots")
	waitOn(t, condition(func() bool { return len(r.agents.started()) == slots }))

	// A run that the loop spawned beyond the free slots exists at once, queued behind the others.
	spawned := 0

	for _, item := range []string{"21", "22", "23", "24"} {
		if _, found, err := latestRun(viewOf(engine.ctx, engine.dir), item); err == nil && found {
			spawned++
		}
	}

	assert.Equal(t, slots, spawned, "the first pass spawns one run for each free slot and no more")

	close(loops)
	require.NoError(t, receive(t, done))
}

func TestQueueLoop_ACrashAfterSpawnAndAResumeStartNoItemTwice(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP1), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP1))
	r.agents.setAct(func(ctx context.Context, item string) error {
		if item == "21" || item == "22" {
			receive(t, ctx.Done())

			return ctx.Err()
		}

		return nil
	})

	first, stop := r.crashEngine(t)

	go func() { _, _ = first.RunFile(testItem, testProject, r.file, modeOff()) }() //nolint:errcheck // the crash ends the run with an error

	waitOn(t, condition(func() bool { return len(r.agents.started()) == 2 }))
	stop()
	require.NoError(t, first.Shutdown())

	r.agents.setAct(nil)

	second := r.engine(t)
	t.Cleanup(func() { require.NoError(t, second.Shutdown()) })

	_, err := second.RunFile(testItem, testProject, r.file, modeOff())
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"21", "22", "23"}, r.agents.started(), "every item started, and none has a second entry")

	for _, item := range []string{"21", "22", "23"} {
		last, found, lookErr := latestRun(viewOf(second.ctx, second.dir), item)
		require.NoError(t, lookErr)
		require.True(t, found)
		assert.Equal(t, 1, last.Attempt, "item %s kept its first attempt", item)
	}
}

// sleptOf reads the seconds that the last execution of a sleep call slept, from the record of the call whose name starts with prefix.
func sleptOf(t *testing.T, records []dbos.StepInfo, prefix string) float64 {
	t.Helper()

	at := slices.IndexFunc(records, func(step dbos.StepInfo) bool {
		return strings.HasPrefix(step.StepName, prefix) && !strings.Contains(step.StepName, "/")
	})
	require.GreaterOrEqual(t, at, 0, "no record of a call named %s", prefix)

	var result stepResult
	require.NoError(t, json.Unmarshal([]byte(fmt.Sprint(records[at].Output)), &result))

	slept, ok := result.Outputs["slept"].(json.Number)
	require.True(t, ok, "the record of %s has no slept field: %v", prefix, result.Outputs)

	seconds, err := slept.Float64()
	require.NoError(t, err)

	return seconds
}

func TestQueueLoop_AnEngineStoppedInTheSleepAfterAFailureWakesTheResumedRunAtTheFirstWakeTimeAndStartsEachItemOnce(t *testing.T) {
	t.Parallel()

	const pause = 8 * time.Second

	r := newLoopRig(t, queueItem("21", model.SeverityP0), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP2))
	r.h.opts.Env["PAUSE_AFTER_FAILURE"] = pause.String()

	first, stop := r.crashEngine(t)
	r.agents.setAct(holdsItem21(func() *Engine { return first }))

	ran := make(chan error, 1)

	go func() {
		_, runErr := first.RunFile(testItem, testProject, r.file, modeOff())
		ran <- runErr
	}()

	// Item 21 failed and item 22 ended, so the pass that starts item 23 sleeps first.
	var wake time.Time

	waitOn(t, condition(func() bool {
		steps, err := dbos.GetWorkflowSteps(first.ctx, testRun)
		if err == nil {
			wake = sleepDeadline(steps)
		}

		return !wake.IsZero() && statusOf(t, first, "item-22-r1") == dbos.WorkflowStatusSuccess
	}))
	stop()
	require.NoError(t, first.Shutdown())
	require.Error(t, receive(t, ran), "the stopped engine ends the run of the first engine")

	client := r.h.client(t, testVersion)
	records, err := client.records(testRun)
	require.NoError(t, err)
	require.Equal(t, wake, sleepDeadline(records), "the engine stopped inside the sleep: its last record is the wake time, and the sleep call has no end record")

	// No engine runs until half the pause is left. A sleep that starts again from the restart sleeps the whole pause.
	waitOn(t, condition(func() bool { return !time.Now().Before(wake.Add(-pause / 2)) }))

	var (
		mu     sync.Mutex
		starts = map[string]time.Time{}
	)

	r.agents.setAct(func(_ context.Context, item string) error {
		mu.Lock()
		defer mu.Unlock()

		starts[item] = time.Now()

		return nil
	})

	second := r.engine(t)
	t.Cleanup(func() { require.NoError(t, second.Shutdown()) })

	_, err = second.RunFile(testItem, testProject, r.file, modeOff())
	require.NoError(t, err)

	mu.Lock()
	started := starts["23"]
	mu.Unlock()

	assert.False(t, started.Before(wake), "item 23 started at %s, before the wake time %s", started, wake)

	records, err = client.records(testRun)
	require.NoError(t, err)
	assert.LessOrEqual(t, sleptOf(t, records, "sleep:pause-"), (pause / 2).Seconds(), "the resumed run slept only to the first wake time, not the whole pause again")
	assert.ElementsMatch(t, []string{"21", "22", "23"}, r.agents.started())

	for _, item := range []string{"21", "22", "23"} {
		assert.Equal(t, 1, r.agents.callsOf(item), "the agent of item %s ran once", item)
	}

	assert.Equal(t, []string{testProject + "|21|hold"}, r.w.statuses, "the failed item was held once")
	require.Len(t, r.w.notes, 1, "the comment on the failed item was written once")
	assert.Contains(t, r.w.notes[0], "|21|")

	steps, err := dbos.GetWorkflowSteps(second.ctx, testRun)
	require.NoError(t, err)
	assert.Equal(t, 1, countSteps(steps, "sleep:pause-"), "the loop slept once")
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
	var engine *Engine

	// Item 22 stays open until the loop has held item 21, so the first pass that ends a run sees the failure of 21 alone and item 23 is still ready.
	r.agents.setAct(holdsItem21(func() *Engine { return engine }))

	engine = r.engine(t)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	_, err := engine.RunFile(testItem, testProject, r.file, modeOff())
	require.NoError(t, err)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)
	assert.Equal(t, 1, countSteps(steps, "sleep:pause-"), "the loop sleeps once, after the pass that saw the failure")
	assert.ElementsMatch(t, []string{"21", "22", "23"}, r.agents.started(), "the items after the failure still run")
	assert.Equal(t, []string{testProject + "|21|hold"}, r.w.statuses, "only the failed item is held")
	require.Len(t, r.w.notes, 1)
	assert.Contains(t, r.w.notes[0], "|21|")
	assert.Contains(t, r.w.notes[0], "The run of this item failed")
	assert.Equal(t, 1, r.agents.callsOf("21"), "a failed item does not start again")
}

// A failed pass with a run still open and nothing ready to start has nothing to protect: no run starts into a shared cause, so the loop does not sleep.
func TestQueueLoop_AFailedPassWithNothingToStartDoesNotSleep(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP0), queueItem("22", model.SeverityP1))

	var engine *Engine

	// Item 22 stays open until the loop has held item 21, so the pass after the failure has an open run and nothing to start.
	r.agents.setAct(holdsItem21(func() *Engine { return engine }))

	engine = r.engine(t)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	_, err := engine.RunFile(testItem, testProject, r.file, modeOff())
	require.NoError(t, err)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"21", "22"}, r.agents.started())
	assert.Equal(t, []string{testProject + "|21|hold"}, r.w.statuses, "only item 21 failed")
	assert.Equal(t, 0, countSteps(steps, "sleep:pause-"), "the pass after the failure starts nothing, so it does not sleep")
}
