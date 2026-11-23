package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
)

func TestRun_OnOpenIsJoinByDefaultAndReplaceWhenAsked(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	code, _, stderr := rig.execute(t.Context(), "run", "12")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{"join"}, rig.runner.onOpens)

	rig = newRig(t)
	code, _, stderr = rig.execute(t.Context(), "run", "12", "--on-open", "replace")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{"replace"}, rig.runner.onOpens)
}

func TestRun_OnOpenGoesToTheEngineThatHoldsTheStateFolder(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--on-open", "replace")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{"replace"}, rig.steering.joinedOnOpens)
	assert.Empty(t, rig.runner.onOpens)
}

func TestRun_AnOnOpenWordThatIsNeitherJoinNorReplaceIsRefusedBeforeAnyEngineOpens(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--on-open", "swap")

	require.Equal(t, 1, code)
	assert.Contains(t, stderr, `on_open is "swap". Use "join" or "replace"`)
	assert.Zero(t, rig.engines)
	assert.Empty(t, rig.runner.onOpens)
	assert.Empty(t, rig.steering.joinedOnOpens)
}

func TestRun_ATakeoverAfterTheEngineStoppedJoinsTheRunItReplacedAndDoesNotReplaceItAgain(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true
	rig.steering.joinErr = flow.ErrEngineGone

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--on-open", "replace")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{"replace"}, rig.steering.joinedOnOpens, "the join replaced the open run")
	assert.Equal(t, []string{"join"}, rig.runner.onOpens, "the takeover continues the new run, so it cancels nothing")
}

func TestRun_ATakeoverThatLosesTheStateFolderBeforeItsOwnStartStillReplaces(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.engineOf = func() error { return flow.ErrStateBusy }

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--on-open", "replace")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{"replace"}, rig.steering.joinedOnOpens, "no start has replaced the run yet, so the join does")
}

func TestRun_AReplaceNamesTheRunItEndsInPlaceOfSayingItContinues(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.pinned = "/work/flow.star"

	code, stdout, stderr := rig.execute(t.Context(), "run", "12", "--on-open", "replace")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 replaces its open run, which pinned /work/flow.star\nitem 12 finished\n", stdout)
}
