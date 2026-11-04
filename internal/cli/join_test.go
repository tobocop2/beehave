package cli_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
)

func TestRun_AnEngineThatHoldsTheStateFolderGetsTheItemAndNoOwnEngineOpens(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 finished\n", stdout)
	assert.Contains(t, stderr, "item 12 joins the engine that runs")
	require.Len(t, rig.steering.joined, 1)
	assert.Equal(t, "12@p "+rig.file, rig.steering.joined[0])
	assert.Zero(t, rig.engines, "a joined run builds no engine")
	assert.Empty(t, rig.runner.items)
	assert.True(t, rig.steering.closeOK)
}

func TestRun_NoEngineOnTheStateFolderMeansNoJoin(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Empty(t, rig.steering.joined)
	assert.Equal(t, []string{"12@p"}, rig.runner.items)
}

func TestRun_AJoinedItemThatStoppedIsAFailedRunForAScript(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true
	rig.steering.joinResult = flow.ItemResult{Stopped: &flow.Stopped{Step: "gate", RC: 3}}

	code, stdout, _ := rig.execute(t.Context(), "run", "12")

	assert.Equal(t, 1, code)
	assert.Contains(t, stdout, "item 12 stopped at step gate with exit code 3")
}

func TestRun_AJoinThatFailsReportsTheErrorAndClosesTheClient(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true
	rig.steering.joinErr = flow.ErrEngineGone

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, flow.ErrEngineGone.Error())
	assert.True(t, rig.steering.closeOK)
}

func TestRun_AJoinThatTheUserStopsSaysHowToContinue(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true
	rig.steering.joinBlock = true

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	code, _, stderr := rig.execute(ctx, "run", "12")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "run beehave run again to continue it")
}

func TestRun_AFailureToTellWhetherAnEngineRunsStopsTheCommand(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.runningErr = errFake

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, errFake.Error())
	assert.Zero(t, rig.engines)
	assert.True(t, rig.steering.closeOK)
}

func TestRun_AnEngineThatTakesTheStateFolderAfterTheCheckIsJoined(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.engineOf = func() error { return flow.ErrStateBusy }

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 finished\n", stdout)
	assert.Equal(t, 1, rig.engines, "the command tried its own engine once")
	assert.Len(t, rig.steering.joined, 1)
}

func TestRun_AClientThatCannotReopenAfterTheBusyEngineIsReported(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.engineOf = func() error { return flow.ErrStateBusy }

	opens := 0
	rig.clientOf = func() error {
		opens++
		if opens == 2 {
			return errFake
		}

		return nil
	}

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, errFake.Error())
	assert.Empty(t, rig.steering.joined)
}

func TestRun_AClientThatCannotCloseAfterTheCheckStopsBeforeAnyEngineOpens(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.closeErr = errFake

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, errFake.Error())
	assert.Zero(t, rig.engines)
}
