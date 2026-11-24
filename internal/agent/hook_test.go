package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

func TestShellCall_ReturnsTheCallOfTheShellToolWithACommand(t *testing.T) {
	t.Parallel()

	call, err := agent.ShellCall("bash", "bash", "/work", "ls")

	require.NoError(t, err)
	assert.Equal(t, agent.HookCall{Dir: "/work", Command: "ls"}, call)
}

func TestShellCall_RefusesAnotherToolAndAnEmptyCommand(t *testing.T) {
	t.Parallel()

	_, err := agent.ShellCall("read", "bash", "/work", "ls")
	require.ErrorContains(t, err, "not a call of the shell tool")

	_, err = agent.ShellCall("bash", "bash", "/work", "")
	require.ErrorContains(t, err, "has no command")

	_, err = agent.ShellCall("", "bash", "", "")
	require.ErrorContains(t, err, "not a call of the shell tool")
}
