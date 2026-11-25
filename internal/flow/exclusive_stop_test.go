package flow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stopsTheEngine is a command that stops the engine on its first run and fails with the stop: the call fails while the engine stops. Every later run succeeds.
func stopsTheEngine(t *testing.T, engine func() *Engine, ran *atomic.Int32) func(context.Context, CommandRequest) error {
	t.Helper()

	return func(context.Context, CommandRequest) error {
		if ran.Add(1) > 1 {
			return nil
		}

		running := engine()

		go func() { assert.NoError(t, running.Shutdown()) }()

		hang(running.ctx.Done())

		return errors.New("the command ended with the stop of the engine")
	}
}

func TestExclusiveStop_ARunWhoseCallFailsWhileTheEngineStopsStaysOpenAndRunsTheCallAgainAtTheNextStart(t *testing.T) {
	t.Parallel()

	// The call is queued by a run of the test item that holds its claim. The next start recovers that run, which holds its claim again before the call runs.
	call := ExclusiveInput{Kind: kindCommand, Run: runID(testItem, 1), RunItem: testItem, Project: testProject, Item: testItem, Request: CommandRequest{Project: testProject, Item: testItem, Run: []string{"call"}}}

	for name, run := range map[string]struct {
		workflow string
		start    func(*Engine) (dbos.WorkflowHandle[ExclusiveOutput], error)
	}{
		"the exclusive run": {exclusiveName, func(e *Engine) (dbos.WorkflowHandle[ExclusiveOutput], error) {
			return dbos.RunWorkflow(e.ctx, e.exclusive, call)
		}},
		"the host job": {hostJobName, func(e *Engine) (dbos.WorkflowHandle[ExclusiveOutput], error) {
			return dbos.RunWorkflow(e.ctx, e.hostJob, call)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			var (
				ran   atomic.Int32
				first atomic.Pointer[Engine]
			)

			engine, err := NewEngine(t.Context(), h.opts, h.deps())
			require.NoError(t, err)

			first.Store(engine)
			require.NoError(t, engine.Launch())

			held := holdRun(t, h, engine, stopsTheEngine(t, first.Load, &ran))

			_, err = run.start(engine)
			require.NoError(t, err)

			// The call fails while the engine stops. Shutdown returns once the run has unwound.
			require.Error(t, receive(t, held.done), "the run of the item ends its wait with the stop of the engine")
			require.NoError(t, engine.Shutdown())

			next := h.engine(t)
			t.Cleanup(func() { held.once.Do(func() { close(held.release) }) })

			var rows []dbos.WorkflowStatus

			waitOn(t, condition(func() bool {
				rows, err = dbos.ListWorkflows(next.ctx, dbos.WithFilterName(run.workflow), dbos.WithFilterLoadOutput(true))

				return err == nil && len(rows) == 1 && rows[0].Status == dbos.WorkflowStatusSuccess
			}))

			text, isText := rows[0].Output.(string)
			require.True(t, isText)
			assert.NotContains(t, text, "shutting down", "the stop is not the saved result of the run: the next start ran the call again")
			assert.NotContains(t, text, `"failure"`, "the run that the next start ran ends with no failure")
			assert.EqualValues(t, 2, ran.Load(), "the call ran at the stop and once more at the next start")
		})
	}
}

func TestExclusiveStop_ACallThatFailsEndsAsTheStopOnlyWhenTheEngineStopped(t *testing.T) {
	t.Parallel()

	// A call of a kind that no run has fails at once with no step, so the test sets the state of the engine and nothing else.
	in := ExclusiveInput{Kind: "none", Project: testProject, Item: testItem}

	for name, run := range map[string]func(*Engine) (ExclusiveOutput, error){
		"the exclusive run": func(e *Engine) (ExclusiveOutput, error) { return e.exclusive(e.ctx, in) },
		"the host job":      func(e *Engine) (ExclusiveOutput, error) { return e.hostJob(e.ctx, in) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			engine := newHarness(t).opened(t)

			out, err := run(engine)

			require.NoError(t, err, "while the engine runs, a failed call is a result of the run")
			require.NotNil(t, out.Failure)
			assert.Contains(t, out.Failure.Text, "no kind")

			require.NoError(t, engine.Shutdown())

			out, err = run(engine)

			require.ErrorIs(t, err, context.Canceled, "once the engine stopped, the failed call is the stop")
			assert.Nil(t, out.Failure, "the stop is no saved failure")
		})
	}
}

func TestTrackGate_OnlyAGateGetsAContextThatTheEndOfTheRunCancels(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).opened(t)

	for kind, tracked := range map[string]bool{kindGate: true, kindMerge: false, kindCommand: false} {
		run, end := engine.trackGate(engine.ctx, ExclusiveInput{Kind: kind})

		require.NoError(t, run.Err(), "the run of kind %s runs until its end", kind)

		end()

		assert.Equal(t, tracked, run.Err() != nil, "the end of a run of kind %s cancels its context only when the kind is tracked", kind)
	}
}

func TestExclusiveStop_ACallThatSucceedsIsSavedAsASuccessAfterTheEngineStopped(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).opened(t)
	require.NoError(t, engine.Shutdown())
	require.Error(t, engine.ctx.Err(), "the engine has stopped")

	out := ExclusiveOutput{Command: CommandOutput{Stdout: "done"}}

	saved, err := engine.endCall(&out, nil)

	require.NoError(t, err)
	assert.Equal(t, "done", saved.Command.Stdout, "a result that the call returned is true after the stop")
	assert.Nil(t, saved.Failure)

	_, err = engine.endCall(&out, errors.New("the call failed"))

	require.ErrorIs(t, err, context.Canceled, "a call that failed while the engine had stopped is the stop")
}
