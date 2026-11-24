package flow

import (
	"context"
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
)

// plannerPause is the time that the fake sleep program takes, so the planner asks again after the workers had time to publish.
const plannerPause = 20 * time.Millisecond

// plannerFile is the workflow of the planner example.
var plannerFile = filepath.Join("..", "..", "examples", "planner", "workflow.star")

// plannerRig runs examples/planner/workflow.star, copied with its prompts, over the fake collaborators. The fake agent of a worker holds until the planner has
// reported what it read, so the read happens while the workers run.
func plannerRig(t *testing.T) (*agentRig, string) {
	t.Helper()

	folder := filepath.Join(t.TempDir(), "planner")
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", "planner"))))

	rig := newAgentRig(t)
	rig.reply("", costing(1.5))
	rig.h.opts.Env = map[string]string{"TASKS": "21 22"}
	rig.h.agents.gate = make(chan struct{})

	var once sync.Once

	rig.h.commands.before = func(ctx context.Context, request CommandRequest) error {
		switch {
		case request.Run[0] == "sleep":
			time.Sleep(plannerPause)
		case request.Run[0] == "echo" && strings.Contains(request.Run[1], "while the workers run"):
			once.Do(func() { close(rig.h.agents.gate) })
		}

		return ctx.Err()
	}

	return rig, filepath.Join(folder, "workflow.star")
}

func TestExamplePlanner_ThePlannerReadsTheProgressOfTheWorkersWhileTheyRun(t *testing.T) {
	t.Parallel()

	rig, file := plannerRig(t)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file, modeOff())

	require.NoError(t, err)

	ran := rig.h.commands.ranBy(testItem)
	require.NotEmpty(t, ran)
	assert.Contains(t, ran, "echo while the workers run: 21=started 22=started", "no agent had ended when the planner read, and each worker had published")
	assert.Equal(t, "echo when the workers ended: 21=done 22=done 21:ok 22:ok cost_usd=3.0", ran[len(ran)-1], "each worker spent 1.5 and the planner adds the cost of its results")
}

func TestExamplePlanner_AWorkerThatPublishedLastShowsItsValueToStatus(t *testing.T) {
	t.Parallel()

	rig, file := plannerRig(t)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file, modeOff())
	require.NoError(t, err)

	got, err := rig.h.client(t, testVersion).Published("item-22-r1")

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "progress", got[0].Key)
	assert.JSONEq(t, `{"stage":"done","cost":1.5}`, string(got[0].Value))
}

// lastRan is the last command that the planner ran: its report.
func lastRan(t *testing.T, h *harness) string {
	t.Helper()

	ran := h.commands.ranBy(testItem)
	require.NotEmpty(t, ran)

	return ran[len(ran)-1]
}

// agentsOf counts the agent starts for one item.
func agentsOf(h *harness, item string) int {
	count := 0

	for _, request := range h.agents.seen() {
		if request.Item == item {
			count++
		}
	}

	return count
}

// runTicking runs the planner example and runs a status tick of the engine every few milliseconds, as the engine does on its own clock. The tick ends
// the local runs that a cancel reached, so a canceled worker gives its agent slot back.
func runTicking(t *testing.T, h *harness, engine *Engine) error {
	t.Helper()

	done := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, plannerFile, modeOff())
		done <- err
	}()

	pace := time.NewTicker(20 * time.Millisecond)
	defer pace.Stop()

	for {
		select {
		case err := <-done:
			return err
		case <-pace.C:
			h.tick(t)
		}
	}
}

// runHasStatus waits until the run has the status, or until ctx ends.
func runHasStatus(ctx context.Context, engine *Engine, run string, want dbos.WorkflowStatusType) error {
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()

	for {
		rows, err := engine.rowsOf([]spawnedRun{{Run: run}})
		if err != nil {
			return err
		}

		if rows[run].Status == want {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func TestExamplePlanner_AStalledWorkerIsCanceledAndItsTaskGoesToANewWorker(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// The planner starts its first watch only after the worker of 21 has ended, so that worker ends before the check-in however slow the host is.
	// The first worker of 22 holds until the cancel ends its context, so the check-in always finds it open.
	// Both workers run as the same login. With one agent for each login, the stalled worker of 22 could take that agent before the worker of 21 and hold it
	// until the cancel. Two agents for each login give each first worker its own agent.
	h.opts.Env = map[string]string{"TASKS": "21 22", "CHECK_IN": "3s", "DEADLINE": "1h"}
	h.opts.PerLogin = 2
	h.agents.stall, h.agents.stallStarts, h.agents.stallFor = "22", 1, time.Hour

	var engine *Engine

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Run[0] == "echo" && strings.Contains(request.Run[1], "while the workers run") {
			return runHasStatus(ctx, engine, "item-21-r1", dbos.WorkflowStatusSuccess)
		}

		return ctx.Err()
	}

	engine = h.engine(t)
	err := runTicking(t, h, engine)

	require.NoError(t, err)
	assert.Equal(t, "echo when the workers ended: 21=done 22=started 21:ok 22:ok cost_usd=0.0", lastRan(t, h), "the first worker of 21 and the second worker of 22 ended well, and the canceled first worker of 22 stopped at started")
	assert.Equal(t, 1, agentsOf(h, "21"), "the worker of 21 ended before the check-in and ran once")
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusOf(t, engine, "item-22-r1"), "the planner canceled the stalled worker")
	assert.Equal(t, dbos.WorkflowStatusSuccess, statusOf(t, engine, "item-22-r2"), "the task of the stalled worker went to a new worker, which ended well")
}

func TestExamplePlanner_TheDeadlineCancelsANewWorkerThatIsStillOpen(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"TASKS": "22", "CHECK_IN": "1s", "DEADLINE": "1h"}
	h.agents.stall, h.agents.stallStarts, h.agents.stallFor = "22", 2, time.Hour

	// The deadline of a gather reads a clock that the test moves past it. A deadline of one second on the real clock could end the wait before the new
	// worker published, and the report would show 22=none.
	var ahead atomic.Int64

	began := time.Now()
	h.reading = func() time.Time { return began.Add(time.Since(began) + time.Duration(ahead.Load())) }

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan error, 1)

	go func() { done <- runTicking(t, h, engine) }()

	// The new worker of 22 has published when its progress exists. The deadline passes only after that.
	waitOn(t, waiting[struct{}]{what: "the new worker of 22 to publish", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		seen, err := client.Published("item-22-r2")

		return struct{}{}, err == nil && len(seen) > 0, nil
	}})

	ahead.Store(int64(2 * time.Hour))

	require.NoError(t, receive(t, done))
	assert.Equal(t, "echo when the workers ended: 22=started 22:deadline cost_usd=0.0", lastRan(t, h))
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusOf(t, engine, "item-22-r2"), "the deadline canceled the new worker")
}

func TestExamplePlanner_WorkersThatEndBeforeTheCheckInNeedNoNewWorker(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"TASKS": "21 22", "CHECK_IN": "1h"}

	engine := h.engine(t)
	err := runTicking(t, h, engine)

	require.NoError(t, err)
	assert.Equal(t, "echo when the workers ended: 21=done 22=done 21:ok 22:ok cost_usd=0.0", lastRan(t, h))
	assert.Equal(t, 1, agentsOf(h, "21"))
	assert.Equal(t, 1, agentsOf(h, "22"), "no worker stalled, so the reassign spawned no item")
}

func TestExamplePlanner_AStallMessageCancelsThatWorkerBeforeTheCheckInEnds(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"TASKS": "21 22", "CHECK_IN": "1h", "DEADLINE": "1h"}
	h.opts.PerLogin = 2
	h.agents.stall, h.agents.stallStarts, h.agents.stallFor = "22", 1, 5*time.Second

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan error, 1)

	go func() { done <- runTicking(t, h, engine) }()

	waitOn(t, waiting[struct{}]{what: "the stall message to be accepted", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		seen, err := client.Published("item-22-r1")
		if err != nil || len(seen) == 0 {
			return struct{}{}, false, nil
		}

		_, err = client.Send(testItem, Outgoing{Topic: "stall", Key: "stall-22", Value: []byte(`"22"`)})

		return struct{}{}, err == nil, nil
	}})

	require.NoError(t, receive(t, done))
	assert.Equal(t, "echo when the workers ended: 21=done 22=started 21:ok 22:ok cost_usd=0.0", lastRan(t, h), "the check-in is one hour, so only the message could stall the worker of 22")
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusOf(t, engine, "item-22-r1"), "the message canceled the stalled worker")
	assert.Equal(t, dbos.WorkflowStatusSuccess, statusOf(t, engine, "item-22-r2"), "the task of the worker went to a new worker")
}

func TestExamplePlanner_TwoWorkersThatEndInOneReadBothKeepTheirOutcomeAndNeedNoNewWorker(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"TASKS": "21 22", "CHECK_IN": "2s", "DEADLINE": "1h"}
	engine := h.engine(t)
	children := []spawnedRun{{Run: "item-21-r1"}, {Run: "item-22-r1"}}

	// The report of the peek runs just before the first first_of, so both workers have ended when that call reads the runs.
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Run[0] != "echo" || !strings.Contains(request.Run[1], "while the workers run") {
			return ctx.Err()
		}

		for {
			rows, err := engine.rowsOf(children)
			if err != nil {
				return err
			}

			if rows["item-21-r1"].Status == dbos.WorkflowStatusSuccess && rows["item-22-r1"].Status == dbos.WorkflowStatusSuccess {
				return ctx.Err()
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	err := runTicking(t, h, engine)

	require.NoError(t, err)
	assert.Equal(t, "echo when the workers ended: 21=done 22=done 21:ok 22:ok cost_usd=0.0", lastRan(t, h))

	names := strings.Join(stepNamesOf(t, engine, runID(testItem, 1)), " ")
	assert.Contains(t, names, "first_of:watch-0#", "the first call read both ended workers")
	assert.NotContains(t, names, "first_of:watch-1#", "the one read took both workers, so the planner made no second call")
	assert.NotContains(t, names, "stalled-22", "the worker of 22 ended in the same read as 21, so the check-in did not stall it")
}

// inCall reports whether the run has a record of the call: the step of the call, or a sub-step of it such as its deadline.
func inCall(engine *Engine, run, call string) bool {
	steps, err := dbos.GetWorkflowSteps(engine.ctx, run)

	return err == nil && slices.ContainsFunc(steps, func(step dbos.StepInfo) bool { return strings.HasPrefix(step.StepName, call) })
}

// receiving reports whether the records of a run stop inside the inbox wait of a first_of call: after the last read of the runs of the call, the library
// recorded the deadline of a receive, and nothing records a received message or the end of the call.
func receiving(steps []dbos.StepInfo, call string) bool {
	look := -1

	for at, step := range steps {
		if strings.HasPrefix(step.StepName, call+lookSuffix) {
			look = at
		}
	}

	if look < 0 {
		return false
	}

	after := steps[look+1:]

	return hasStep(after, sleepStep) && !hasStep(after, receiveStepName) && !hasStep(steps, call)
}

// watchOne is the step of the second first_of call of the planner, the call that the crash test stops in.
const watchOne = "first_of:watch-1#2"

// countName counts the step names that equal name.
func countName(names []string, name string) int {
	return len(slices.DeleteFunc(slices.Clone(names), func(one string) bool { return one != name }))
}

func TestExamplePlanner_AnEngineStoppedInFirstOfTakesTheStallMessageOfTheDownTimeOnceAfterTheRestart(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"TASKS": "21 22", "CHECK_IN": "1h", "DEADLINE": "1h"}
	h.opts.PerLogin = 2
	// The first two agent starts of 22 hold. The first is the worker of 22 before the stop. The second is that worker again after the restart, when its
	// recovery starts the agent before the message cancels it, or else the new worker, which then ends after the hold. Either way the first worker ends only by the cancel.
	h.agents.stall, h.agents.stallStarts, h.agents.stallFor = "22", 2, 5*time.Second

	stopped, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopped, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	ran := make(chan error, 1)

	go func() {
		_, runErr := first.RunFile(testItem, testProject, plannerFile, modeOff())
		ran <- runErr
	}()

	// The worker of 21 ended in watch-0, so the planner waits in the receive of watch-1 for the worker of 22 or a stall message.
	waitOn(t, condition(func() bool {
		steps, stepErr := dbos.GetWorkflowSteps(first.ctx, runID(testItem, 1))

		return stepErr == nil && receiving(steps, watchOne)
	}))
	stop()
	require.NoError(t, first.Shutdown())
	require.Error(t, receive(t, ran), "the stopped engine ends the call of the first engine")

	client := h.client(t, testVersion)
	records, err := client.records(runID(testItem, 1))
	require.NoError(t, err)
	require.True(t, receiving(records, watchOne), "the engine stopped in the receive of watch-1: it took no worker end and no message before the stop")

	_, err = client.Send(testItem, Outgoing{Topic: "stall", Key: "stall-22", Value: []byte(`"22"`)})
	require.NoError(t, err, "the message lands while no engine runs")

	second := h.engine(t)

	require.NoError(t, runTicking(t, h, second))
	assert.Equal(t, "echo when the workers ended: 21=done 22=started 21:ok 22:ok cost_usd=0.0", lastRan(t, h), "the run ends as the run with no stop does")
	assert.Equal(t, 1, agentsOf(h, "21"), "the worker of 21 ended before the stop and did not run again")
	assert.Len(t, slices.DeleteFunc(h.commands.ranBy(testItem), func(ran string) bool { return !strings.Contains(ran, "while the workers run") }), 1,
		"the step before first_of ran once")
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusOf(t, second, "item-22-r1"), "the message canceled the stalled worker")
	assert.Equal(t, dbos.WorkflowStatusSuccess, statusOf(t, second, "item-22-r2"), "the task of the worker went to one new worker")

	last, found, err := latestRun(viewOf(second.ctx, second.dir), "22")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 2, last.Attempt, "the planner took the message once, so it gave the task of 22 to one new worker")

	names := stepNamesOf(t, second, runID(testItem, 1))
	assert.Equal(t, 1, countName(names, watchOne), "the call that the stop cut ended once, on the message")
	assert.Equal(t, 1, countName(names, "cancel:named-22#1"), "the planner canceled the worker of 22 once")
	assert.False(t, inCall(second, runID(testItem, 1), "first_of:watch-2#"), "no worker was left open, so the planner made no later call")
	assert.NotContains(t, strings.Join(names, " "), "stalled-", "the message, not the check-in, stalled the worker")
}
