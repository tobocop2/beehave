//go:build !linux

package proc

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserProcesses_CountsTheProcessesOfTheUserWithTheSystemPs(t *testing.T) {
	t.Parallel()

	got, err := userProcesses()

	require.NoError(t, err)
	assert.Positive(t, got, "this test process is one of them")
}

func TestCountByPs_FailsWhenPsCannotRun(t *testing.T) {
	t.Parallel()

	_, err := countByPs("/nonexistent/ps", os.Getuid())

	require.Error(t, err)
}
