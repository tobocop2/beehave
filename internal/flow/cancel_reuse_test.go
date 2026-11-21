package flow

import (
	"sync/atomic"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listHook is a store whose list of workflows runs a function once, before or after the read. Each other read goes to the store.
type listHook struct {
	dbos.Context

	armed atomic.Bool
	// before is true when the function runs before the read, so the read sees the state that the function made.
	before bool
	run    func()
}

func (l *listHook) ListWorkflows(_ dbos.Client, opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	if l.before && l.armed.CompareAndSwap(true, false) {
		l.run()
	}

	rows, err := dbos.ListWorkflows(l.Context, opts...)

	if !l.before && l.armed.CompareAndSwap(true, false) {
		l.run()
	}

	return rows, err
}

// endedOf is the channel that closes when the run that the engine lists under id returns.
func endedOf(engine *Engine, id string) chan struct{} {
	engine.cancels.mu.Lock()
	defer engine.cancels.mu.Unlock()

	if a, found := engine.cancels.runs[id]; found {
		return a.ended
	}

	return nil
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// resumeRig starts an engine whose store runs the hook once, before or after the read of the canceled runs in a tick.
func resumeRig(t *testing.T, before bool) (*harness, *Engine, *Client, *listHook) {
	t.Helper()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)

	hook := &listHook{before: before}

	engine := harness.engineWith(t, func(e *Engine) {
		hook.Context = e.ctx
		e.ctx = hook
	})

	return harness, engine, harness.client(t, testVersion), hook
}

// resumeAndList resumes the run of the item and waits until the engine lists the resumed attempt under the run id of the earlier one.
func resumeAndList(t *testing.T, engine *Engine, client *Client, earlier chan struct{}) chan struct{} {
	t.Helper()

	// The attempt ends before the store records the end of the run, and resume reads the store.
	assert.Eventually(t, func() bool {
		rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterWorkflowIDs(testRun), dbos.WithFilterStatus(endedStatuses...))

		return err == nil && len(rows) == 1
	}, soonBound, pollEvery, "the store records the end of the earlier attempt")

	if err := client.Resume(testItem); err != nil {
		t.Error(err)

		return nil
	}

	var later chan struct{}

	assert.Eventually(t, func() bool {
		later = endedOf(engine, testRun)

		return later != earlier
	}, soonBound, pollEvery, "the engine lists the resumed attempt")

	return later
}

func TestCancel_TheTickThatForgetsTheEndedEarlierAttemptLeavesTheResumedRunListed(t *testing.T) {
	t.Parallel()

	harness, engine, client, hook := resumeRig(t, true)
	harness.agents.err = errFake

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err, "the first attempt fails at the agent and stays listed as ended")

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	earlier := endedOf(engine, testRun)
	require.True(t, isClosed(earlier))

	var later chan struct{}

	hook.run = func() { later = resumeAndList(t, engine, client, earlier) }
	hook.armed.Store(true)

	harness.tick(t)

	require.NotNil(t, later, "the run resumed while the tick listed the canceled runs")
	waitOn(t, ownerAsks(t, harness))
	require.False(t, isClosed(later), "the resumed run waits for its owner")
	assert.Equal(t, later, endedOf(engine, testRun), "the tick left the resumed attempt listed")
}

func TestCancel_ACancelRowOfTheEarlierAttemptDoesNotStopTheResumedRun(t *testing.T) {
	t.Parallel()

	harness, engine, client, hook := resumeRig(t, false)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))
	require.NoError(t, client.Cancel(testItem), "the store marks the first attempt canceled, and the tick has not stopped it")

	earlier := endedOf(engine, testRun)

	var later chan struct{}

	// The first attempt ends and the run resumes after the tick read the canceled row and before it acts on it.
	hook.run = func() {
		stopRun(engine, testRun)
		assert.Eventually(t, func() bool { return isClosed(earlier) }, soonBound, pollEvery, "the first attempt ends")

		later = resumeAndList(t, engine, client, earlier)
	}
	hook.armed.Store(true)

	harness.tick(t)

	require.Error(t, receive(t, done).err, "the first attempt ended at its cancel")
	require.NotNil(t, later)

	waitOn(t, ownerAsks(t, harness))
	require.NoError(t, client.Reply(testItem, Reply{Text: "go on"}))
	waitOn(t, waiting[struct{}]{what: "the resumed run to end done", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		rows, err := client.Status()

		return struct{}{}, err == nil && rows[0].State == runDone, nil
	}})
}
