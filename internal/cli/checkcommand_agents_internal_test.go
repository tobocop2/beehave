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

func TestCheckCommand_AnAllowedCommandExits0AndACallOfAnotherToolExits2InTheHookFormatOfEachAgent(t *testing.T) {
	t.Parallel()

	for name, tool := range shellTools {
		code, stderr := hookOf(t, name, eventsOf(t, tool, "go test ./...", "/work")[name])
		assert.Zero(t, code, "%s: %s", name, stderr)

		code, stderr = hookOf(t, name, eventsOf(t, "read", "git add -A", "/work")[name])
		assert.Equal(t, 2, code, name)
		assert.Contains(t, stderr, "the hook event is not a call of the shell tool", name)
	}
}

func TestCheckCommand_AShellCallWithNoCommandExits2InTheHookFormatOfEachAgent(t *testing.T) {
	t.Parallel()

	noCommand := map[model.Agent][]string{
		claude.Name:   {`{"tool_name":"Bash"}`, `{"tool_name":"Bash","tool_input":{}}`, `{"tool_name":"Bash","tool_input":{"command":""}}`},
		opencode.Name: {`{"tool":"bash"}`, `{"tool":"bash","args":{}}`, `{"tool":"bash","args":{"command":""}}`, `{"directory":"/work","tool":"bash","args":null}`},
	}

	for name, events := range noCommand {
		for _, event := range events {
			code, stderr := hookOf(t, name, event)

			assert.Equal(t, 2, code, "%s %s", name, event)
			assert.Contains(t, stderr, "the shell call of the hook event has no command", "%s %s", name, event)
		}
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

	code, stderr := hookOf(t, opencode.Name, eventsOf(t, "Bash", "git add -A", "/work")[claude.Name])
	assert.Equal(t, 2, code, "a Claude Code event names no OpenCode tool, so it is no shell call of OpenCode")
	assert.Contains(t, stderr, "the hook event is not a call of the shell tool")

	code, stderr = hookOf(t, claude.Name, eventsOf(t, "bash", "git add -A", "/work")[opencode.Name])
	assert.Equal(t, 2, code, "an OpenCode event names no Claude Code tool, so it is no shell call of Claude Code")
	assert.Contains(t, stderr, "the hook event is not a call of the shell tool")
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
