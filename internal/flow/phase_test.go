package flow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockedHandle is a phase handle whose result never comes.
type blockedHandle struct {
	dbos.WorkflowHandle[PhaseOutput]
}

func (blockedHandle) GetResult(...dbos.GetResultOption) (PhaseOutput, error) {
	select {}
}

func TestFirstRenewal_AStoppedEngineEndsTheWaitWithACancelThatKeepsTheCause(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.beats.failFor = "owner-a"
	harness.beats.err = errors.New("tracker busy")
	engine := idleEngine(t, harness)

	ctx, stop := context.WithCancelCause(t.Context())
	stopped := assert.AnError
	stop(stopped)

	err := engine.firstRenewal(ctx, "run-a", ItemInput{Item: testItem, Project: testProject}, "owner-a")

	require.ErrorIs(t, err, context.Canceled, "the library records a stopped run, not a failed one")
	require.ErrorIs(t, err, stopped)
}

func TestAwaitPhase_AStoppedEngineEndsTheWaitWithACancelThatKeepsTheCause(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	ctx, stop := dbos.WithCancelCause(rig.h.engine(t).ctx)
	stopped := assert.AnError
	stop(stopped)

	_, err := awaitResult(ctx, ctx, blockedHandle{}, nil)

	require.ErrorIs(t, err, context.Canceled, "the library records a stopped run, not a failed one")
	require.ErrorIs(t, err, stopped)
}

// shutdownHandle is a phase handle whose phase already ended with the shutdown cause of the library: the child run saw the stop of the engine
// and recorded it before the context of the waiting run was canceled.
type shutdownHandle struct {
	dbos.WorkflowHandle[PhaseOutput]
}

func (shutdownHandle) GetResult(...dbos.GetResultOption) (PhaseOutput, error) {
	return PhaseOutput{}, errors.New("DBOS is shutting down")
}

func TestAwaitPhase_APhaseThatEndedWithTheShutdownOfAStoppedEngineLeavesTheRunOpen(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	waiting := rig.h.engine(t).ctx // not canceled: the stop of the engine reaches the context of the run after the child run saw it

	engineCtx, stopEngine := dbos.WithCancelCause(waiting)
	stopped := assert.AnError
	stopEngine(stopped)

	_, err := awaitResult(waiting, engineCtx, shutdownHandle{}, nil)

	require.ErrorIs(t, err, context.Canceled, "the library records a stopped run, not a failed one")
	require.ErrorIs(t, err, stopped)
}

func TestPhaseInput_AStoredStepWithoutTheEditsKeyKeepsEditsAndANoEditsStepStaysWithout(t *testing.T) {
	t.Parallel()

	var old PhaseInput
	require.NoError(t, json.Unmarshal([]byte(`{"run":"r1","item":"i1"}`), &old))
	assert.False(t, old.NoEdits, "a step recorded before the key existed keeps the edit mode")

	raw, err := json.Marshal(PhaseInput{Run: "r1", Kind: "review", NoEdits: true})
	require.NoError(t, err)

	var replayed PhaseInput
	require.NoError(t, json.Unmarshal(raw, &replayed))
	assert.True(t, replayed.NoEdits, "a replayed edits = False step does not gain the edit mode")
}
