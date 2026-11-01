package flow

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/pool"
	"github.com/tobocop2/obee/internal/workflow"
)

// poolLogins is the real pick of the pool over the runners names: equal use everywhere, so the first runner in config order wins a tie.
// It records the busy logins that the engine gave for each item.
type poolLogins struct {
	mu      sync.Mutex
	runners []model.Runner
	busy    map[string][]string
	picked  map[string]string
	err     error
}

func newPoolLogins(names ...string) *poolLogins {
	runners := make([]model.Runner, 0, len(names))
	for _, name := range names {
		runners = append(runners, model.Runner{Name: name, Agent: model.AgentClaude})
	}

	return &poolLogins{runners: runners, busy: map[string][]string{}, picked: map[string]string{}}
}

func (p *poolLogins) Pick(ctx context.Context, _, item string, busy []string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.err != nil {
		return "", p.err
	}

	read := func(context.Context, model.Runner) model.Reading {
		return model.Known(model.Usage{FiveHour: model.Window{Percent: 10}, SevenDay: model.Window{Percent: 10}})
	}

	runner, err := pool.Select(ctx, p.runners, config.Pool{FiveHourMax: 90, SevenDayMax: 95}, model.AgentClaude, read, busy...)
	if err != nil {
		return "", err
	}

	p.busy[item] = slices.Clone(busy)
	p.picked[item] = runner.Name

	return runner.Name, nil
}

func (p *poolLogins) loginOf(item string) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.picked[item]
}

func (p *poolLogins) busyOf(item string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.busy[item]
}

// overlap counts the agents that run at once. Each agent waits up to its window for another to start, so a serial loop shows as a peak of one.
type overlap struct {
	// window is how long a step waits for another step to start. A test that expects two steps to overlap sets it to the event
	// timeout: the step ends as soon as the second one starts. A test that expects no overlap sets it to the time it watches.
	window time.Duration
	mu     sync.Mutex
	now    int
	peak   int
}

func (o *overlap) act(ctx context.Context, _ string) error {
	o.mu.Lock()
	o.now++
	o.peak = max(o.peak, o.now)
	o.mu.Unlock()

	deadline := time.Now().Add(o.window)

	for time.Now().Before(deadline) {
		o.mu.Lock()
		enough := o.peak >= 2
		o.mu.Unlock()

		if enough || ctx.Err() != nil {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	o.mu.Lock()
	o.now--
	o.mu.Unlock()

	return nil
}

func (o *overlap) peakValue() int {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.peak
}

func TestHeldLogin_APhaseAndARunWithAnEntryHoldTheirLogin(t *testing.T) {
	t.Parallel()

	login, err := heldLogin(dbos.WorkflowStatus{Name: phaseName, Input: `{"login":"alice"}`})
	require.NoError(t, err)
	assert.Equal(t, "alice", login)

	login, err = heldLogin(dbos.WorkflowStatus{Name: itemName, Input: `{"login":"bob","entry":"work"}`})
	require.NoError(t, err)
	assert.Equal(t, "bob", login)

	login, err = heldLogin(dbos.WorkflowStatus{Name: itemName, Input: `{"login":"carol"}`})
	require.NoError(t, err)
	assert.Empty(t, login, "a run that a user started has no entry and holds no login")

	login, err = heldLogin(dbos.WorkflowStatus{Name: itemName, Input: `{}`})
	require.NoError(t, err)
	assert.Empty(t, login)
}

func TestHeldLogin_AnInputThatIsNotJSONIsAnError(t *testing.T) {
	t.Parallel()

	_, err := heldLogin(dbos.WorkflowStatus{ID: "w-1", Name: phaseName, Input: "not json"})

	require.ErrorContains(t, err, "read the input of workflow w-1")
}

func TestHeldLogins_ARowWithABadInputFailsTheWholeRead(t *testing.T) {
	t.Parallel()

	rows := []dbos.WorkflowStatus{
		{Name: phaseName, Input: `{"login":"alice"}`},
		{ID: "w-2", Name: phaseName, Input: "not json"},
	}

	held, err := heldLogins(rows, nil)

	require.ErrorContains(t, err, "read the input of workflow w-2")
	assert.Nil(t, held)
}

func TestHeldLogins_TakenAndHeldLoginsJoinSortedWithoutRepeats(t *testing.T) {
	t.Parallel()

	rows := []dbos.WorkflowStatus{
		{Name: phaseName, Input: `{"login":"bob"}`},
		{Name: itemName, Input: `{"login":"carol","entry":"work"}`},
		{Name: itemName, Input: `{"login":"alice","entry":"work"}`},
	}

	held, err := heldLogins(rows, []string{"bob", "dave"})

	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "bob", "carol", "dave"}, held)
}

func TestFreeLogin_ALoginThatOnlyAQueuedRunHoldsIsBusy(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t)
	r.h.opts.MaxConcurrent = 1
	release := make(chan struct{})
	started := make(chan struct{}, 1)

	r.agents.setAct(func(ctx context.Context, _ string) error {
		started <- struct{}{}

		select {
		case <-release:
		case <-ctx.Done():
		}

		return nil
	})

	engine := r.engine(t)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })
	t.Cleanup(func() { close(release) })

	pinned, err := workflow.Pin(r.file)
	require.NoError(t, err)

	enqueue := func(item, login string) {
		in := ItemInput{Item: item, Project: testProject, File: r.file, Files: pinned, Login: login, Entry: "work"}
		_, runErr := dbos.RunWorkflow(engine.ctx, engine.item, in, dbos.WithQueue(engine.itemQueue), dbos.WithWorkflowID(runID(item, 1)))
		require.NoError(t, runErr)
	}

	enqueue("21", "alice")
	<-started

	// The one slot of the items queue is taken, so the run of item 22 waits ENQUEUED. No phase and no other run holds its login.
	enqueue("22", "bob")

	busy, err := engine.busyLogins(nil)

	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "bob"}, busy, "a run that waits in the items queue holds its login")
}

// A run that obee run started holds its login only while one of its phases is open. Between phases its login reads free,
// and a second step on it waits for the per-login limit. This is a known gap: it is slow, not wrong.
func TestFreeLogin_APlainRunHoldsNoLoginBetweenPhases(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP0))
	picks := newPoolLogins("alice", "bob")
	r.h.picker = picks
	release := make(chan struct{})

	r.agents.setAct(func(ctx context.Context, _ string) error {
		select {
		case <-release:
		case <-ctx.Done():
		}

		return nil
	})

	engine := r.engine(t)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	done := make(chan error, 1)

	go func() { _, err := engine.RunFile(testItem, testProject, r.file); done <- err }()

	require.True(t, waitUntil(func() bool { return picks.loginOf("21") != "" }), "item 21 started")
	close(release)
	require.NoError(t, <-done)

	assert.NotContains(t, picks.busyOf("21"), picks.loginOf(testItem), "the loop run that a user started has no phase open, so its login reads free")
}

func TestFreeLogin_APickFailsWhenTheOpenWorkflowsCannotBeRead(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	require.NoError(t, engine.Shutdown())

	_, err := engine.pickLogin(t.Context(), ItemInput{Item: "21", Project: testProject})

	require.ErrorContains(t, err, "read the busy logins for item 21")
}

func TestFreeLogin_TwoLoginsAndTwoReadyItemsRunBothAgentsAtOnce(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP0), queueItem("22", model.SeverityP1))
	picks := newPoolLogins("alice", "bob")
	r.h.picker = picks
	seen := &overlap{window: eventTimeout}
	r.agents.setAct(seen.act)

	require.NoError(t, r.run(t))

	assert.Equal(t, 2, seen.peakValue(), "both agent steps ran at the same time")
	assert.NotEqual(t, picks.loginOf("21"), picks.loginOf("22"), "each item got its own login")
}

func TestFreeLogin_OneLoginRunsTheAgentStepsOneAtATime(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP0), queueItem("22", model.SeverityP1))
	r.h.picker = newPoolLogins("alice")
	seen := &overlap{window: time.Second}
	r.agents.setAct(seen.act)

	require.NoError(t, r.run(t))

	assert.Equal(t, 1, seen.peakValue(), "one login runs one agent step at a time")
	assert.ElementsMatch(t, []string{"21", "22"}, r.agents.started(), "both items still ran")
}

func TestFreeLogin_ALoginThatHoldsARunStaysBusyUntilItEnds(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP0), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP2))
	picks := newPoolLogins("alice", "bob")
	r.h.picker = picks
	release := make(chan struct{})

	r.agents.setAct(func(ctx context.Context, item string) error {
		if item != "21" {
			return nil
		}

		select {
		case <-release:
		case <-ctx.Done():
		}

		return nil
	})

	engine := r.engine(t)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	done := make(chan error, 1)

	go func() { _, err := engine.RunFile(testItem, testProject, r.file); done <- err }()

	require.True(t, waitUntil(func() bool { return picks.loginOf("23") != "" }), "the third item started after the second ended")
	close(release)
	require.NoError(t, <-done)

	assert.Equal(t, picks.loginOf("22"), picks.loginOf("23"), "item 23 took the login that item 22 freed, not the login of the item that still runs")
	assert.Contains(t, picks.busyOf("23"), picks.loginOf("21"), "the login of the running item is busy")
	assert.NotContains(t, picks.busyOf("23"), picks.loginOf("22"), "the login of the ended item is free")
}

func TestFreeLogin_AResumeKeepsTheRecordedLogins(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP1), queueItem("22", model.SeverityP1))
	first := newPoolLogins("alice", "bob")
	r.h.picker = first
	r.agents.setAct(func(ctx context.Context, item string) error {
		if item == "21" || item == "22" {
			<-ctx.Done()

			return ctx.Err()
		}

		return nil
	})

	crashed, stop := r.crashEngine(t)

	go func() { _, _ = crashed.RunFile(testItem, testProject, r.file) }() //nolint:errcheck // the crash ends the run with an error

	require.True(t, waitUntil(func() bool { return len(r.agents.started()) == 2 }), "both agents run when the engine stops")
	stop()
	require.NoError(t, crashed.Shutdown())

	before := map[string]string{"21": first.loginOf("21"), "22": first.loginOf("22")}
	assert.NotEqual(t, before["21"], before["22"])

	later := newPoolLogins("carol", "dave")
	r.h.picker = later
	r.agents.setAct(nil)

	resumed := r.engine(t)
	t.Cleanup(func() { require.NoError(t, resumed.Shutdown()) })

	_, err := resumed.RunFile(testItem, testProject, r.file)
	require.NoError(t, err)

	for item, login := range before {
		last, found, lookErr := latestRun(resumed.ctx, item)
		require.NoError(t, lookErr)
		require.True(t, found)

		in, inErr := last.input()
		require.NoError(t, inErr)
		assert.Equal(t, login, in.Login, "item %s kept the login of the first pick", item)
		assert.Empty(t, later.loginOf(item), "the resume picked no login for item %s", item)
	}
}
