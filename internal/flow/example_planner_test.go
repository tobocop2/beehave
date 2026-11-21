package flow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)

	ran := rig.h.commands.ranBy(testItem)
	require.NotEmpty(t, ran)
	assert.Contains(t, ran, "echo while the workers run: 21=started 22=started", "no agent had ended when the planner read, and each worker had published")
	assert.Equal(t, "echo when the workers ended: 21=done 22=done 21:ok 22:ok cost_usd=3.0", ran[len(ran)-1], "each worker spent 1.5 and the planner adds the cost of its results")
}

func TestExamplePlanner_AWorkerThatPublishedLastShowsItsValueToStatus(t *testing.T) {
	t.Parallel()

	rig, file := plannerRig(t)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
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
		_, err := engine.RunFile(testItem, testProject, plannerFile)
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

func TestExamplePlanner_AStalledWorkerIsCanceledAndItsTaskGoesToANewWorker(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// The worker of 21 does not stall, so it ends well before the check-in, even on a slow host. The worker of 22 holds past the check-in.
	// Both workers run as the same login. With one agent for each login, the stalled worker of 22 could take that agent before the worker of 21 and hold it
	// past the check-in. Two agents for each login give each first worker its own agent, so 21 ends before the check-in in every order of the two starts.
	h.opts.Env = map[string]string{"TASKS": "21 22", "CHECK_IN": "3s", "DEADLINE": "1h"}
	h.opts.PerLogin = 2
	h.agents.stall, h.agents.stallStarts, h.agents.stallFor = "22", 1, 5*time.Second

	engine := h.engine(t)
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
	h.opts.Env = map[string]string{"TASKS": "22", "CHECK_IN": "1s", "DEADLINE": "1s"}
	h.agents.stall, h.agents.stallStarts, h.agents.stallFor = "22", 2, 3*time.Second

	engine := h.engine(t)
	err := runTicking(t, h, engine)

	require.NoError(t, err)
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
