package cli_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
)

func TestRun_DebounceIsNoWindowByDefaultAndTheWindowWhenAsked(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	code, _, stderr := rig.execute(t.Context(), "run", "12")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []time.Duration{0}, rig.runner.debounces)

	rig = newRig(t)
	code, _, stderr = rig.execute(t.Context(), "run", "12", "--debounce", "30s")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []time.Duration{30 * time.Second}, rig.runner.debounces)
}

func TestRun_DebounceGoesToTheEngineThatHoldsTheStateFolder(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--debounce", "30s")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []time.Duration{30 * time.Second}, rig.steering.joinedDebounces)
	assert.Empty(t, rig.runner.debounces)
}

func TestRun_ADebounceThatIsNoWindowIsRefusedBeforeAnyEngineOpens(t *testing.T) {
	t.Parallel()

	for text, sentence := range map[string]string{
		"0s":   `debounce is "0s", which must be above zero`,
		"-5s":  `debounce is "-5s", which must be above zero`,
		"soon": `debounce is "soon", which is not a duration`,
	} {
		rig := newRig(t)

		code, _, stderr := rig.execute(t.Context(), "run", "12", "--debounce", text)

		require.Equal(t, 1, code)
		assert.Contains(t, stderr, sentence)
		assert.Zero(t, rig.engines)
		assert.Empty(t, rig.runner.debounces)
		assert.Empty(t, rig.steering.joinedDebounces)
	}
}

func TestRun_ATakeoverAfterTheEngineStoppedContinuesTheDelayedRunAndDoesNotHoldItBackAgain(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true
	rig.steering.joinErr = flow.ErrEngineGone

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--debounce", "30s")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []time.Duration{30 * time.Second}, rig.steering.joinedDebounces, "the join held the start back")
	assert.Equal(t, []time.Duration{0}, rig.runner.debounces, "the takeover continues the delayed run, so it does not push the window back")
}
