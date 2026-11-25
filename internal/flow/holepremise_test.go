package flow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/require"
)

// These tests pin what DBOS leaves in the step ids of a run, which the resume reads to find a refused step:
// a receive or an event read that finds its message waiting records no deadline step and leaves one id with no record,
// and a refused step with a refused failure record leaves two.

var errHoleFirstRun = errors.New("the first run fails after the agent")

// holeWait sends a message to the run itself and receives it, or sets an event of the run and reads it. Either finds its message waiting.
func holeWait(ctx dbos.Context, id string, useEvent bool) error {
	if useEvent {
		if err := dbos.SetEvent(ctx, "topic", "answer"); err != nil {
			return err
		}

		_, err := dbos.GetEvent[string](ctx, id, "topic", 0)

		return err
	}

	if err := dbos.Send(ctx, id, "answer", "topic"); err != nil {
		return err
	}

	_, err := dbos.Recv[string](ctx, "topic", 0)

	return err
}

// holeWorkflow is a run with a look at the caps, a wait that finds its message waiting, an agent step, and a step that fails on its first run only.
// The wait is Recv of a message that the run sent to itself, or GetEvent of an event that the run set; both take two ids and record one.
func holeWorkflow(agents, fails *atomic.Int32, useEvent bool) dbos.Workflow[int, int] {
	return func(ctx dbos.Context, _ int) (int, error) {
		id, err := ctx.GetWorkflowID()
		if err != nil {
			return 0, err
		}

		if _, err := dbos.RunAsStep(ctx, func(context.Context) (int, error) { return 0, nil }, dbos.WithStepName(capLookStepName)); err != nil {
			return 0, err
		}

		if err := holeWait(ctx, id, useEvent); err != nil {
			return 0, err
		}

		if _, err := dbos.RunAsStep(ctx, func(context.Context) (int, error) { agents.Add(1); return 1, nil }, dbos.WithStepName(agentStepName)); err != nil {
			return 0, err
		}

		return dbos.RunAsStep(ctx, func(context.Context) (int, error) {
			if fails.Add(1) == 1 {
				return 0, errHoleFirstRun
			}

			return 7, nil
		}, dbos.WithStepName("after"))
	}
}

// failedHoleRun runs holeWorkflow until its last step fails, and returns the context and the id of the run.
func failedHoleRun(t *testing.T, agents *atomic.Int32, useEvent bool) (dbos.Context, string) {
	t.Helper()

	var fails atomic.Int32

	flow := holeWorkflow(agents, &fails, useEvent)
	flowCtx := premiseContext(t, flow)

	handle, err := dbos.RunWorkflow(flowCtx, flow, 0, dbos.WithWorkflowID("hole-run"))
	require.NoError(t, err)

	_, err = resultWithin(t, handle)
	require.ErrorIs(t, err, errHoleFirstRun)

	return flowCtx, "hole-run"
}

// idRuns returns the width of each run of step ids with no record, in the order of the history.
func idRuns(steps []dbos.StepInfo) []int {
	var runs []int

	next := 0

	for _, step := range steps {
		if step.StepID > next {
			runs = append(runs, step.StepID-next)
		}

		next = step.StepID + 1
	}

	return runs
}

func TestPremise_AReceiveThatFindsItsMessageWaitingLeavesOneStepIDWithNoRecord(t *testing.T) {
	t.Parallel()

	var agents atomic.Int32

	flowCtx, id := failedHoleRun(t, &agents, false)

	steps, err := dbos.GetWorkflowSteps(flowCtx, id)
	require.NoError(t, err)
	require.Equal(t, []int{1}, idRuns(steps))
}

func TestPremise_AnEventReadInAWorkflowThatFindsItsEventWaitingLeavesOneStepIDWithNoRecord(t *testing.T) {
	t.Parallel()

	var agents atomic.Int32

	flowCtx, id := failedHoleRun(t, &agents, true)

	steps, err := dbos.GetWorkflowSteps(flowCtx, id)
	require.NoError(t, err)
	require.Equal(t, []int{1}, idRuns(steps))
}
