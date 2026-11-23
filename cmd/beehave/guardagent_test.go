package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/agent/opencode"
	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/model"
)

// guardRun runs `beehave check command --agent <name>` through the real command table, with the event on stdin, and returns the exit code and stderr.
func guardRun(t *testing.T, name model.Agent, event string) (int, string) {
	t.Helper()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	_, err = writer.WriteString(event)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	previous := os.Stdin
	os.Stdin = reader

	t.Cleanup(func() {
		os.Stdin = previous

		require.NoError(t, reader.Close())
	})

	var stdout, stderr bytes.Buffer

	env := map[string]string{"HOME": t.TempDir()}
	code := cli.Execute(t.Context(), []string{"check", "command", "--agent", string(name)}, buildInfo(env, "dev", cli.Flow{}), env, &stdout, &stderr)

	return code, stderr.String()
}

// guardEvents is the hook input of each agent for the shell call of one command, in the format the adapter of that agent sends.
var guardEvents = map[model.Agent]func(command string) string{
	claude.Name: func(command string) string {
		return `{"hook_event_name":"PreToolUse","cwd":"/work","tool_name":"Bash","tool_input":{"command":"` + command + `"}}`
	},
	opencode.Name: func(command string) string {
		return `{"directory":"/work","tool":"bash","args":{"command":"` + command + `"}}`
	},
}

func TestGuardAgent_TheSameRefusedCommandInTheFormatOfEachAgentExits2AndShowsTheRefusal(t *testing.T) { //nolint:paralleltest // the test replaces os.Stdin of the process
	for name, event := range guardEvents {
		code, stderr := guardRun(t, name, event("git add -A"))

		assert.Equal(t, 2, code, name)
		assert.Contains(t, stderr, "beehave refused the command: this command breaks the shell rules of the step.\nbulk-staging: git add -A", name)
	}
}

func TestGuardAgent_AnAllowedCommandExits0InTheFormatOfEachAgent(t *testing.T) { //nolint:paralleltest // the test replaces os.Stdin of the process
	for name, event := range guardEvents {
		code, stderr := guardRun(t, name, event("go test ./..."))

		assert.Zero(t, code, "%s: %s", name, stderr)
	}
}

func TestGuardAgent_AMalformedInputExits2InTheFormatOfEachAgent(t *testing.T) { //nolint:paralleltest // the test replaces os.Stdin of the process
	for name := range guardEvents {
		code, stderr := guardRun(t, name, "not json")

		assert.Equal(t, 2, code, name)
		assert.Contains(t, stderr, "beehave refused the command", name)
	}
}

func TestAdapters_EveryAgentOfTheTableHasAHookReader(t *testing.T) {
	t.Parallel()

	readers := newAdapters(nil, nil, nil).hooks()

	assert.Len(t, readers, len(guardEvents))

	for name := range guardEvents {
		assert.NotNil(t, readers[name], name)
	}
}
