package cli_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGateHold_EachCommandPassesTheItemToTheClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		call string
		out  string
	}{
		{"hold", []string{"gate", "hold", "12"}, "gate-hold 12", ""},
		{"release", []string{"gate", "release", "12"}, "gate-release 12", ""},
		{"priority", []string{"gate", "priority", "12", "P0"}, "gate-priority 12 P0", "The gate of item 12 now has the place of P0."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)

			code, stdout, stderr := rig.execute(t.Context(), test.args...)

			require.Equal(t, 0, code, stderr)
			assert.Equal(t, []string{test.call}, rig.steering.calls)
			assert.Contains(t, stdout, test.out)
		})
	}
}

func TestGateHold_AClientRefusalReachesTheOwner(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"gate", "hold", "12"}, {"gate", "release", "12"}, {"gate", "priority", "12", "P1"}} {
		rig := newRig(t)
		rig.steering.err = errors.New("the gate is held already")

		code, stdout, stderr := rig.execute(t.Context(), args...)

		assert.NotEqual(t, 0, code)
		assert.Contains(t, stderr, "the gate is held already")
		assert.NotContains(t, stdout, "now has the place")
	}
}

func TestGateHold_ArgumentsThatAreNotAnItemAndALevelAreRefused(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"hold with no item":        {"gate", "hold"},
		"hold with a bad item":     {"gate", "hold", "no spaces"},
		"release with two items":   {"gate", "release", "1", "2"},
		"priority with no level":   {"gate", "priority", "12"},
		"priority with a bad item": {"gate", "priority", "no spaces", "P0"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)

			code, _, _ := rig.execute(t.Context(), args...)

			assert.NotEqual(t, 0, code)
			assert.Empty(t, rig.steering.calls)
		})
	}
}

func TestGateHold_TheCommandsStandBesideTheOwnProgramGate(t *testing.T) {
	t.Parallel()

	rig := newOwnRig(t)

	code, _, stderr := rig.run(t.Context(), "gate", "hold", "12")

	assert.Equal(t, 0, code, stderr)
	assert.Zero(t, rig.judged)
}
