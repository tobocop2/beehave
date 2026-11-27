package flow

import (
	"context"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// treeFlow runs one agent step in the parent and then in a child that spawn starts. The child runs two agent steps.
const treeFlow = `def worker(item):
    agent(model = "m", prompt = "prompts/a.md", name = "x")
    agent(model = "m", prompt = "prompts/a.md", name = "y")

def main(item):
    agent(model = "m", prompt = "prompts/a.md", name = "a")
    gather(spawn(items = ["21"], entry = "worker", name = "fan"), name = "fan")
    command(name = "after", run = ["after"])
`

const childRun = "item-21-r1"

// stoppedRun returns the progress of the run when a cap holds a step of it back, or nil.
func stoppedRun(t *testing.T, client *Client, run string) *CapStop {
	t.Helper()

	steps, err := client.Steps(run)
	if err != nil {
		return nil
	}

	return steps.CapStop
}

// holdOnCap runs the flow file until a cap holds a step back, then stops the engine and returns the stop. The run stays open.
func holdOnCap(t *testing.T, rig *agentRig, file string) *CapStop {
	t.Helper()

	engine := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	done := runInBackground2(engine, file)

	waitOn(t, waiting[struct{}]{what: "the run to stop on a cap", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, stoppedRun(t, client, testRun) != nil, nil }})

	stop := stoppedRun(t, client, testRun)

	require.NoError(t, engine.Shutdown())
	receive(t, done)

	return stop
}

func TestBudget_TheTreeCapCountsTheRoundAndEveryChild(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxTreeBudget = 5
	rig.reply("", costing(2.5))

	engine := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	done := runInBackground2(engine, capFile(t, treeFlow))

	// The parent spent 2.5, the child spent 2.5 in x: the sum is at the cap when y is about to start.
	waitOn(t, waiting[struct{}]{what: "the child run to stop on a cap", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, stoppedRun(t, client, childRun) != nil, nil }})

	stop := stoppedRun(t, client, childRun)
	assert.Equal(t, scopeTree, stop.Scope)
	assert.InDelta(t, 5.0, stop.Cap, 0)
	assert.InDelta(t, 5.0, stop.Spent, 0)
	assert.Len(t, rig.h.agents.seen(), 2, "the step y did not start")

	steps, err := client.Steps(childRun)
	require.NoError(t, err)
	assert.Equal(t, runRunning, steps.State)

	require.NoError(t, engine.Shutdown())
	receive(t, done)
}

func TestBudget_AChildInheritsTheRootCap(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxTreeBudget = 4
	rig.reply("", costing(3))

	engine := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	done := runInBackground2(engine, capFile(t, treeFlow))

	// The child has no cap of its own and no spend of its own at x: the sum of the root is 3, under the cap of 4, so x starts. The sum of 6 stops y.
	waitOn(t, waiting[struct{}]{what: "the child run to stop on a cap", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, stoppedRun(t, client, childRun) != nil, nil }})

	stop := stoppedRun(t, client, childRun)
	assert.InDelta(t, 4.0, stop.Cap, 0, "the child uses the cap of the root")
	assert.InDelta(t, 6.0, stop.Spent, 0, "the sum holds the cost of the root and of the child")
	assert.Nil(t, stoppedRun(t, client, testRun), "the root run is not stopped: only the step of the child is")

	require.NoError(t, engine.Shutdown())
	receive(t, done)
}

// deepTree runs a tree of three item runs: item 1 is the root, item 2 its child and item 3 the grandchild. Each run records the cost of 2 dollars the way an
// agent call does, and starts the run below it. The engine starts a child by spawn, and a run that spawn started cannot spawn, so only a workflow of the
// test builds the third level. The outputs that a run records come from outputsOf, by the item. It returns the engine, whose store holds the finished tree.
func deepTree(t *testing.T, rig *agentRig) *Engine {
	t.Helper()

	return deepTreeOf(t, rig, func(string) map[string]any { return spendOutputs(agent.Spend{Cost: 2}) })
}

func deepTreeOf(t *testing.T, rig *agentRig, outputsOf func(item string) map[string]any) *Engine {
	t.Helper()

	engine := rig.h.opened(t)

	below := map[string]string{"1": "2", "2": "3"}

	var tier func(ctx dbos.Context, in ItemInput) (string, error)

	tier = func(ctx dbos.Context, in ItemInput) (string, error) {
		_, err := runStep(engine, ctx, "agent#1/spent0", func(context.Context) (stepResult, error) {
			return stepResult{Outputs: outputsOf(in.Item)}, nil
		})

		next, more := below[in.Item]
		if err != nil || !more {
			return in.Item, err
		}

		handle, err := dbos.RunWorkflow(ctx, tier, ItemInput{Item: next, Project: in.Project}, dbos.WithWorkflowID(runID(next, 1)))
		if err != nil {
			return "", err
		}

		return handle.GetResult()
	}

	dbos.RegisterWorkflow(engine.ctx, tier, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(engine.ctx))

	handle, err := dbos.RunWorkflow(engine.ctx, tier, ItemInput{Item: "1", Project: testProject}, dbos.WithWorkflowID(runID("1", 1)))
	require.NoError(t, err)

	_, err = handle.GetResult()
	require.NoError(t, err)

	return engine
}

func TestBudget_TheTreeSumReachesAGrandchildAndTheCapStopsTheTree(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxTreeBudget = 5
	engine := deepTree(t, rig)

	// A step of the grandchild asks for the sum of its tree. The climb from the grandchild passes the child and ends at the root.
	root, err := rootRun(viewOf(engine.ctx, engine.dir), runID("3", 1))
	require.NoError(t, err)
	assert.Equal(t, runID("1", 1), root)

	// Only the cost of the grandchild takes the sum from 4 to 6, past the cap of 5.
	spent, err := runAndBelowSpend(viewOf(engine.ctx, engine.dir), root)
	require.NoError(t, err)
	assert.InDelta(t, 6.0, spent.Cost, 0, "the sum holds the root, the child and the grandchild")

	stop, err := overCap("q", engine.treeCap(), spent)
	require.NoError(t, err)
	require.NotNil(t, stop, "the cap stops the tree")
	assert.Equal(t, scopeTree, stop.Scope)
	assert.InDelta(t, 6.0, stop.Spent, 0)

	child, err := runAndBelowSpend(viewOf(engine.ctx, engine.dir), runID("2", 1))
	require.NoError(t, err)
	assert.InDelta(t, 4.0, child.Cost, 0, "a run sums itself and every run below it")
}

// unreadableChild is a tree in which the child records a cost that is a string, as a damaged record does.
func unreadableChild(t *testing.T) (*Engine, agent.Spend) {
	t.Helper()

	rig := newAgentRig(t)
	rig.h.opts.MaxTreeBudget = 5
	engine := deepTreeOf(t, rig, func(item string) map[string]any {
		if item == "2" {
			return map[string]any{costField: "2"}
		}

		return spendOutputs(agent.Spend{Cost: 2})
	})

	spent, err := runAndBelowSpend(viewOf(engine.ctx, engine.dir), runID("1", 1))
	require.NoError(t, err)

	return engine, spent
}

func TestBudget_ATreeWhoseChildRecordedAnUnreadableCostStopsTheNextStep(t *testing.T) {
	t.Parallel()

	engine, spent := unreadableChild(t)

	assert.True(t, spent.Unknown, "the cost of the child is not known")

	stop, err := overCap("q", engine.treeCap(), spent)

	assert.Nil(t, stop)
	require.Error(t, err, "the next step does not start under a cap that cannot be checked")
	assert.Contains(t, err.Error(), "agents.max_tree_budget_usd")
}

func TestBudget_TheStopForAnUnreadableRecordNamesTheRecordNotThePriceOfTheModel(t *testing.T) {
	t.Parallel()

	engine, spent := unreadableChild(t)

	_, err := overCap("q", engine.treeCap(), spent)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "recorded cost")
	assert.Contains(t, err.Error(), "cannot be read")
	assert.NotContains(t, err.Error(), "no price")

	_, err = overCap("q", engine.treeCap(), agent.Spend{Unknown: true})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no price", "an unpriced model keeps its own cause")
}

// capRestart runs three steps of 3 dollars under a cap that stops the third, reads the stop on the run and on the line of the step, raises the cap and
// starts a new engine, and checks that the run finishes with no cancel and that no step ran twice. The cap after the stop is `after`: 0 removes the cap.
func capRestart(t *testing.T, set func(h *harness, limit float64), scope string, after float64) {
	t.Helper()

	rig := newAgentRig(t)
	set(rig.h, 5)
	rig.reply("", costing(3))

	deps := rig.h.deps()
	first, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, first.Launch())
	t.Cleanup(func() { require.NoError(t, first.Shutdown()) })

	client := rig.h.client(t, testVersion)
	done := runInBackground2(first, capFile(t, threeFlow))

	waitOn(t, waiting[struct{}]{what: "the run to stop on a cap", run: runOfEngine(t, first), read: func() (struct{}, bool, error) { return struct{}{}, stoppedRun(t, client, testRun) != nil, nil }})

	steps, err := client.Steps(testRun)
	require.NoError(t, err)

	require.NotNil(t, steps.CapStop)
	assert.Equal(t, scope, steps.CapStop.Scope)
	assert.InDelta(t, 5.0, steps.CapStop.Cap, 0)
	assert.InDelta(t, 6.0, steps.CapStop.Spent, 0)
	assert.Equal(t, runRunning, steps.State, "the run is open, not failed")

	line := steps.Steps[len(steps.Steps)-1]
	require.NotNil(t, line.CapStop, "the line of the held step shows the stop")
	assert.InDelta(t, 6.0, line.CapStop.Spent, 0)
	assert.Empty(t, line.Error, "the step is not recorded as failed")
	assert.Len(t, rig.h.agents.seen(), 2)

	require.NoError(t, first.Shutdown())
	require.Error(t, receive(t, done).err, "the stopped engine ends the call and leaves the run open")

	set(rig.h, after)

	second := engineOn(t, rig.h, deps)
	handle, err := dbos.RetrieveWorkflow[ItemResult](second.ctx, testRun)
	require.NoError(t, err, "the launch of the second engine recovers the open run")

	_, err = resultOfHandle(t, handle)

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 3, "a and b are not run again after the restart: c runs once")
	assert.Equal(t, []string{"done"}, rig.h.commands.ran())
	assert.Nil(t, stoppedRun(t, client, testRun), "no stop is left after the cap is raised")
}

func TestBudget_ATreeCapStopWaitsForARaiseAndARestartFinishesTheRound(t *testing.T) {
	t.Parallel()

	capRestart(t, func(h *harness, limit float64) { h.opts.MaxTreeBudget = limit }, scopeTree, 20)
}

func TestBudget_ATreeCapSetToZeroAfterAStopLetsTheRoundFinish(t *testing.T) {
	t.Parallel()

	capRestart(t, func(h *harness, limit float64) { h.opts.MaxTreeBudget = limit }, scopeTree, 0)
}

func TestBudget_ARunCapSetToZeroAfterAStopLetsTheRoundFinish(t *testing.T) {
	t.Parallel()

	capRestart(t, func(h *harness, limit float64) { h.opts.MaxRunBudget = limit }, scopeRun, 0)
}

func TestBudget_ARunCapStopWaitsForARaiseAndARestartFinishesTheRound(t *testing.T) {
	t.Parallel()

	capRestart(t, func(h *harness, limit float64) { h.opts.MaxRunBudget = limit }, scopeRun, 20)
}
