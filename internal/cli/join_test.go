package cli_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
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

func sandboxedProject() string {
	return strings.Replace(validProject, `test_report = "r.xml")`, `test_report = "r.xml", sandbox = "on")`, 1)
}

// clientProjects holds the project file of the client for each sandbox key, and the mode a run of it stores.
var clientProjects = map[string]struct {
	project string
	stored  model.SandboxMode
}{
	"key on":    {sandboxedProject(), model.SandboxOn},
	"key off":   {strings.Replace(validProject, `test_report = "r.xml")`, `test_report = "r.xml", sandbox = "off")`, 1), model.SandboxOff},
	"key unset": {validProject, model.SandboxOn},
}

func TestRun_AJoinCarriesTheSandboxModeOfTheProjectOfTheClient(t *testing.T) {
	t.Parallel()

	for name, test := range clientProjects {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)
			writeConfig(t, rig.dir, "projects/p.star", test.project)
			rig.steering.running = true

			code, _, stderr := rig.execute(t.Context(), "run", "12")

			require.Equal(t, 0, code, stderr)
			assert.Equal(t, []model.SandboxMode{test.stored}, rig.steering.joinedSandboxes, "an unset key stores on, never an empty mode")
		})
	}
}

func TestRun_AnOwnEngineRunCarriesTheSandboxModeOfTheProject(t *testing.T) {
	t.Parallel()

	for name, test := range clientProjects {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)
			writeConfig(t, rig.dir, "projects/p.star", test.project)

			code, _, stderr := rig.execute(t.Context(), "run", "12")

			require.Equal(t, 0, code, stderr)
			assert.Equal(t, []model.SandboxMode{test.stored}, rig.runner.sandboxes, "an unset key stores on, never an empty mode")
		})
	}
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
	rig.steering.joinErr = errFake

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, errFake.Error())
	assert.True(t, rig.steering.closeOK)
	assert.Zero(t, rig.engines, "a join that fails for another cause starts no engine")
}

func TestRun_AJoinWhoseEngineStoppedTakesOverTheItemOnItsOwnEngine(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true
	rig.steering.joinErr = flow.ErrEngineGone

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 finished\n", stdout)
	assert.Contains(t, stderr, "the engine that ran item 12 stopped; this command continues the run")
	assert.Equal(t, 1, rig.engines)
	assert.Equal(t, []string{"12@p"}, rig.runner.items)
	assert.True(t, rig.steering.closeOK)
}

func TestRun_ATakeoverThatLosesTheStateFolderJoinsTheEngineThatWonIt(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true
	rig.steering.joinErrs = []error{flow.ErrEngineGone}
	rig.engineOf = func() error { return flow.ErrStateBusy }

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 finished\n", stdout)
	assert.Equal(t, 1, strings.Count(stderr, "this command continues the run"))
	assert.Equal(t, 1, rig.engines, "the takeover tried its own engine once")
	assert.Len(t, rig.steering.joined, 2, "the first join saw the engine gone, the second joined the engine that won")
	assert.Empty(t, rig.runner.items)
}

func TestRun_AJoinWhoseEngineStoppedAndWhoseUserStoppedTheCommandDoesNotTakeOver(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true
	rig.steering.joinErr = flow.ErrEngineGone

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	code, _, stderr := rig.execute(ctx, "run", "12")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "run beehave run again to continue it")
	assert.Zero(t, rig.engines)
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
