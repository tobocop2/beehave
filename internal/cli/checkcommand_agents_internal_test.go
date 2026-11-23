package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/agent/opencode"
	"github.com/tobocop2/beehave/internal/model"
)

// shellTools is the name that each agent gives its shell tool.
var shellTools = map[model.Agent]string{claude.Name: "Bash", opencode.Name: "bash"}

// eventsOf returns the hook input of each agent for one tool call that runs command in dir.
func eventsOf(t *testing.T, tool, command, dir string) map[model.Agent]string {
	t.Helper()

	claudeEvent, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "cwd": dir, "tool_name": tool, "tool_input": map[string]string{"command": command}})
	require.NoError(t, err)

	openEvent, err := json.Marshal(map[string]any{"directory": dir, "tool": tool, "args": map[string]string{"command": command}})
	require.NoError(t, err)

	return map[model.Agent]string{claude.Name: string(claudeEvent), opencode.Name: string(openEvent)}
}

func TestCheckCommand_TheSameRefusedCommandInTheHookFormatOfEachAgentExits2AndPrintsTheRefusal(t *testing.T) {
	t.Parallel()

	for name, tool := range shellTools {
		code, stderr := hookOf(t, name, eventsOf(t, tool, "git add -A", "/work")[name])

		assert.Equal(t, 2, code, name)
		assert.Contains(t, stderr, "beehave refused the command: this command breaks the shell rules of the step.\nbulk-staging: git add -A", name)
	}
}

func TestCheckCommand_AnAllowedCommandAndACallOfAnotherToolExit0InTheHookFormatOfEachAgent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    model.Agent
		tool    string
		command string
	}{
		{claude.Name, "Bash", "go test ./..."},
		{opencode.Name, "bash", "go test ./..."},
		{claude.Name, "Read", "git add -A"},
		{opencode.Name, "read", "git add -A"},
	}

	for _, tc := range cases {
		code, stderr := hookOf(t, tc.name, eventsOf(t, tc.tool, tc.command, "/work")[tc.name])

		assert.Zero(t, code, "%s %s: %s", tc.name, tc.tool, stderr)
	}
}

func TestCheckCommand_AMalformedInputExits2InTheHookFormatOfEachAgent(t *testing.T) {
	t.Parallel()

	wrongType := map[model.Agent]string{claude.Name: `{"tool_name":1}`, opencode.Name: `{"tool":1}`}

	for name, typed := range wrongType {
		for _, event := range []string{"", "not json", "null", "[1]", typed} {
			code, stderr := hookOf(t, name, event)

			assert.Equal(t, 2, code, "%s %q", name, event)
			assert.Contains(t, stderr, "beehave refused the command", "%s %q", name, event)
		}
	}
}

func TestCheckCommand_TheHookFormatOfAnotherAgentIsNotRead(t *testing.T) {
	t.Parallel()

	code, _ := hookOf(t, opencode.Name, eventsOf(t, "Bash", "git add -A", "/work")[claude.Name])
	assert.Zero(t, code, "a Claude Code event names no OpenCode tool, so it is no shell call of OpenCode")

	code, _ = hookOf(t, claude.Name, eventsOf(t, "bash", "git add -A", "/work")[opencode.Name])
	assert.Zero(t, code, "an OpenCode event names no Claude Code tool, so it is no shell call of Claude Code")
}

func TestCheckCommand_ADirectoryOfTheCallInTheFormatOfEachAgentPlacesARelativePath(t *testing.T) {
	t.Parallel()

	args := []string{"--forbid", "protected-path", "--protect", "/own/checkout"}

	for name, tool := range shellTools {
		code, _ := hookOf(t, name, eventsOf(t, tool, "touch a", "/own/checkout")[name], args...)
		assert.Equal(t, 2, code, "%s: a relative write in the protected directory", name)

		code, _ = hookOf(t, name, eventsOf(t, tool, "touch a", "/work/step")[name], args...)
		assert.Zero(t, code, "%s: a relative write in another directory", name)
	}
}

func TestCheckCommand_AMissingOrUnknownAgentIsRefusedWithTheKnownOnes(t *testing.T) {
	t.Parallel()

	for _, name := range []model.Agent{"", "gemini"} {
		code, stderr := hookOf(t, name, cleanEvent)

		assert.Equal(t, 2, code, name)
		assert.Contains(t, stderr, `unknown agent "`+string(name)+`" (use one of: claude, opencode)`, name)
	}
}
