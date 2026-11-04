//go:build !darwin

package seatbelt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/sandbox"
	"github.com/tobocop2/beehave/internal/sandbox/seatbelt"
)

func TestEnforcer_CannotRunOutsideMacOS(t *testing.T) {
	t.Parallel()

	e := seatbelt.New()
	assert.Equal(t, seatbelt.Name, e.Name())
	require.ErrorIs(t, e.Check(), sandbox.ErrUnavailable)

	_, err := e.Run(t.Context(), sandbox.Plan{}, 0, sandbox.Command{})
	require.ErrorIs(t, err, sandbox.ErrUnavailable)
}
