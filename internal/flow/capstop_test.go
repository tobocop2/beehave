package flow

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapStop_TheStatusNoteNamesTheScopeTheCapAndTheSpend(t *testing.T) {
	t.Parallel()

	stop := CapStop{Scope: scopeTree, Cap: 5, Spent: 6.25}

	assert.Equal(t, "stopped at the tree cap $5, spent $6.25", stop.text())
}

func TestCapStop_ARunHeldAtACapShowsTheNoteInItsStatusLines(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	rig.reply("", costing(3))

	stop := holdOnCap(t, rig, capFile(t, threeFlow))
	require.NotNil(t, stop)

	notes := RunProgress{CapStop: stop}.Notes()

	assert.Equal(t, []string{"stopped at the run cap $5, spent $6"}, notes)
}

// stoppedTree returns a finished tree of three item runs whose engine is shut down, so that every read of the store fails.
func stoppedTree(t *testing.T) *Engine {
	t.Helper()

	rig := newAgentRig(t)
	engine := deepTree(t, rig)
	require.NoError(t, engine.Shutdown())

	return engine
}

// failingRead is a context of the engine whose store fails at one numbered read, counted over the reads of runs and of steps from 1.
// With empty set, the read finds no rows instead of failing.
type failingRead struct {
	dbos.Context

	at    int
	empty bool
	seen  int
}

func (f *failingRead) hit() bool {
	f.seen++

	return f.seen == f.at
}

func (f *failingRead) ListWorkflows(_ dbos.Client, opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	if f.hit() {
		if f.empty {
			return nil, nil
		}

		return nil, errors.New("the store failed")
	}

	return dbos.ListWorkflows(f.Context, opts...)
}

func (f *failingRead) GetWorkflowSteps(_ dbos.Client, id string, opts ...dbos.GetWorkflowStepsOption) ([]dbos.StepInfo, error) {
	if f.hit() {
		return nil, errors.New("the store failed")
	}

	return dbos.GetWorkflowSteps(f.Context, id, opts...)
}

func TestCapStop_TheTreeSumReportsTheReadThatFailedAtEachLevel(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))
	root := runID("1", 1)

	// The sum of the root reads: steps of 1, children of 1, steps of 2, children of 2, steps of 3, children of 3.
	for read, words := range map[int]string{
		1: "read the steps of " + root,
		2: "list the item runs",
		3: "read the steps of " + runID("2", 1),
		4: "list the item runs",
		5: "read the steps of " + runID("3", 1),
		6: "list the item runs",
	} {
		_, err := runAndBelowSpend(&failingRead{Context: engine.ctx, at: read}, root)
		require.ErrorContains(t, err, words, "read %d", read)
		require.ErrorContains(t, err, "the store failed", "read %d", read)
	}

	spent, err := runAndBelowSpend(&failingRead{Context: engine.ctx}, root)
	require.NoError(t, err, "no read fails")
	assert.InDelta(t, 6.0, spent.Cost, 0)
}

func TestCapStop_TheClimbReportsAReadThatFailedAboveTheRun(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))

	// The climb from the grandchild reads the run, then its parent.
	_, err := rootRun(&failingRead{Context: engine.ctx, at: 1}, runID("3", 1))
	require.ErrorContains(t, err, "the store failed")

	_, err = rootRun(&failingRead{Context: engine.ctx, at: 2}, runID("3", 1))
	require.ErrorContains(t, err, "the store failed")
}

func TestCapStop_TheClimbEndsAtTheRunWhenNoItemRunIsAboveIt(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))

	// The run names a parent, but the read of the parent finds no item run: the run is the root.
	root, err := rootRun(&failingRead{Context: engine.ctx, at: 2, empty: true}, runID("3", 1))

	require.NoError(t, err)
	assert.Equal(t, runID("3", 1), root)
}

func TestCapStop_ACapCheckWithNoCapReadsNoSum(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))
	// Every read of the store fails at its first read, so a check that read a sum would fail.
	failing := &Engine{ctx: &failingRead{Context: engine.ctx, at: 1}}

	got, err := failing.checkCaps("q", runID("1", 1))

	require.NoError(t, err)
	assert.Nil(t, got.Stop)
}

func TestCapStop_ARunWhoseClimbToTheRootFailsCannotSumItsTree(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))
	failing := &Engine{ctx: &failingRead{Context: engine.ctx, at: 1}}

	_, err := failing.treeSpend(runID("1", 1))

	require.ErrorContains(t, err, "the store failed")
}

func TestCapStop_ACapCheckWhoseTreeReadFailsReturnsTheErrorAndNoStop(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))
	// The tree cap is set and the first read of the climb fails.
	failing := &Engine{ctx: &failingRead{Context: engine.ctx, at: 1}, maxTreeBudget: 5}

	got, err := failing.checkCaps("q", runID("1", 1))

	require.ErrorContains(t, err, "the store failed")
	assert.Nil(t, got.Stop, "no stop comes with the error")
}

func TestCapStop_AStepHeldForTwoLooksRecordsEachLookUnderItsOwnNumber(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	rig.reply("", costing(3))

	engine := rig.h.engine(t)
	done := runInBackground2(engine, capFile(t, threeFlow))

	var looks []string

	// The names that holdAtCap builds: the agent step name, "/budget", the try, a dot and the look. Try 1 is the first try of each step.
	lookNames := map[string]bool{}

	for _, step := range []string{"agent:a#1", "agent:b#2", "agent:c#3"} {
		for look := range 10 {
			lookNames[step+"/budget1."+strconv.Itoa(look)] = true
		}
	}

	waitOn(t, waiting[struct{}]{what: "four looks of the caps to be recorded", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		records, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
		if err != nil {
			return struct{}{}, false, nil
		}

		looks = looks[:0]

		for _, record := range records {
			if lookNames[record.StepName] {
				looks = append(looks, record.StepName)
			}
		}

		return struct{}{}, len(looks) >= 4, nil
	}})

	require.NoError(t, engine.Shutdown())
	receive(t, done)

	assert.Equal(t, []string{
		"agent:c#3/budget1.0", "agent:c#3/budget1.1", "agent:c#3/budget1.2", "agent:c#3/budget1.3",
	}, looks[:4], "a and b passed and record no look; the held step c records looks 0, 1, 2 and 3 in order")
}

func TestCapStop_AStoreThatRefusesTheRecordOfTheStopFailsTheStepWithNoAgentRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	rig.reply("", costing(3))

	engine := rig.h.engine(t)

	// a and b spend $6, so the look of the phase of c stops it, and the store refuses the record of that stop in the item run.
	_, err := sqlOf(t, rig.h).ExecContext(t.Context(), "CREATE TRIGGER refuse_stop BEFORE INSERT ON operation_outputs "+
		"WHEN NEW.function_name = 'agent:c#3/budget1.0' BEGIN SELECT RAISE(ABORT, 'the store refused the record'); END")
	require.NoError(t, err)

	_, err = engine.RunFile(testItem, testProject, capFile(t, threeFlow), modeOff())

	require.ErrorContains(t, err, "the store refused the record")
	assert.Len(t, rig.h.agents.seen(), 2, "c does not run past the cap")
}

func TestCapStop_AWaitForACapThatEndsWithItsRunNamesTheItemAndTheCause(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	// The wait is an hour, so only the end of the context of the run can end it.
	rig.h.capWait = time.Hour
	engine := rig.h.opened(t)

	// The step runs on a context of its own that the test ends once the run is inside the sleep, not on a timer.
	ends := make(chan context.CancelFunc, 1)
	hold := func(ctx dbos.Context, in ItemInput) (string, error) {
		own, cancel := dbos.WithCancel(ctx)
		defer cancel()

		ends <- cancel

		s := &script{e: engine, ctx: own, in: in}

		err := s.holdAtCap("q", 1, 0, &CapStop{Scope: scopeRun, Cap: 5, Spent: 6})
		if err == nil {
			return "", errors.New("the hold did not fail")
		}

		return err.Error(), nil
	}

	dbos.RegisterWorkflow(engine.ctx, hold, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(engine.ctx))

	handle, err := dbos.RunWorkflow(engine.ctx, hold, ItemInput{Item: "1", Project: testProject}, dbos.WithWorkflowID(testRun))
	require.NoError(t, err)

	cancel := receive(t, ends)

	waitOn(t, inASleep(t, engine))
	cancel()

	words, err := handle.GetResult()
	require.NoError(t, err)
	assert.Equal(t, "wait for a cap of item 1 to be raised: context canceled", words)
}

func TestCapStop_ACapCheckWhoseRunReadFailsReturnsTheErrorAndNoStop(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))
	// The run cap is set and the read of the steps of the item run fails.
	failing := &Engine{ctx: &failingRead{Context: engine.ctx, at: 1}, maxRunBudget: 5}

	got, err := failing.checkCaps("q", runID("1", 1))

	require.ErrorContains(t, err, "read the steps of "+runID("1", 1))
	require.ErrorContains(t, err, "the store failed")
	assert.Nil(t, got.Stop, "no stop comes with the error")
}

func TestCapStop_AReadThatFailsReachesTheCallerWithItsWords(t *testing.T) {
	t.Parallel()

	engine := stoppedTree(t)

	_, err := runAndBelowSpend(engine.ctx, runID("1", 1))
	require.ErrorContains(t, err, "read the steps of "+runID("1", 1))

	_, err = itemRuns(engine.ctx, dbos.WithFilterParentWorkflowID(runID("1", 1)))
	require.ErrorContains(t, err, "list the item runs")

	_, err = rootRun(engine.ctx, runID("3", 1))
	require.ErrorContains(t, err, "list the item runs")
}
