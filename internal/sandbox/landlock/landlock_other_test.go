//go:build !linux

package landlock_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/sandbox"
	"github.com/tobocop2/beehave/internal/sandbox/landlock"
)

func TestEnforcer_CannotRunOutsideLinux(t *testing.T) {
	t.Parallel()

	e := landlock.New()
	assert.Equal(t, landlock.Name, e.Name())
	require.ErrorIs(t, e.Check(), sandbox.ErrUnavailable)

	_, err := e.Run(t.Context(), sandbox.Plan{}, 0, sandbox.Command{})
	require.ErrorIs(t, err, sandbox.ErrUnavailable)
}

func TestRunHelper_IsNeverTheHelperOutsideLinux(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer

	code, helper := landlock.RunHelper([]string{"beehave"}, &stderr)
	assert.False(t, helper)
	assert.Zero(t, code)
	assert.Empty(t, stderr.String())
}
