package proc_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/proc"
)

func TestExecRunner_StdinIsTheTextTheProgramReads(t *testing.T) {
	t.Parallel()

	got := proc.ExecRunner{Binary: "cat", Stdin: "line one\nline two\n"}.Run(t.Context(), nil)

	assert.Equal(t, 0, got.ExitCode)
	assert.Equal(t, "line one\nline two\n", got.Stdout)
}

func TestExecRunner_NoStdinGivesTheProgramAnEmptyInput(t *testing.T) {
	t.Parallel()

	got := proc.ExecRunner{Binary: "cat"}.Run(t.Context(), nil)

	assert.Equal(t, 0, got.ExitCode)
	assert.Empty(t, got.Stdout)
}

func TestExecRunner_OnlyReplacesTheEnvironmentOfTheProcessAndEnvAddsToIt(t *testing.T) {
	t.Setenv("BEEHAVE_TEST_SECRET", "secret")

	got := proc.ExecRunner{Binary: "env", Only: []string{"A=1"}, Env: []string{"B=2"}}.Run(t.Context(), nil)

	require.Equal(t, 0, got.ExitCode)
	assert.Equal(t, []string{"A=1", "B=2"}, strings.Fields(got.Stdout), "no variable of the process reaches the program")
}

func TestExecRunner_AnEmptyOnlyGivesAnEmptyEnvironment(t *testing.T) {
	t.Setenv("BEEHAVE_TEST_SECRET", "secret")

	got := proc.ExecRunner{Binary: "env", Only: []string{}}.Run(t.Context(), nil)

	require.Equal(t, 0, got.ExitCode)
	assert.Empty(t, got.Stdout)
}

func TestExecRunner_WithoutOnlyTheProgramKeepsTheEnvironmentOfTheProcess(t *testing.T) {
	t.Setenv("BEEHAVE_TEST_KEPT", "yes")

	got := proc.ExecRunner{Binary: "env"}.Run(t.Context(), nil)

	assert.Contains(t, got.Stdout, "BEEHAVE_TEST_KEPT=yes")
}
