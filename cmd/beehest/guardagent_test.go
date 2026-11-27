package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/agent/claude"
	"github.com/tobocop2/beehest/internal/agent/opencode"
	"github.com/tobocop2/beehest/internal/cli"
	"github.com/tobocop2/beehest/internal/model"
)

// guardRun runs `beehest check command --agent <name>` through the real command table, with the event on stdin, and returns the exit code and stderr.
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
		assert.Contains(t, stderr, "beehest refused the command: this command breaks the shell rules of the step.\nbulk-staging: git add -A", name)
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
		assert.Contains(t, stderr, "beehest refused the command", name)
	}
}

func TestGuardAgent_AnEventThatIsNotAShellCallWithACommandExits2InTheFormatOfEachAgent(t *testing.T) { //nolint:paralleltest // the test replaces os.Stdin of the process
	events := map[model.Agent][]string{
		claude.Name:   {`{}`, `{"tool_name":"Bash"}`, `{"tool_name":"Bash","tool_input":{}}`, guardEvents[opencode.Name]("ls")},
		opencode.Name: {`{}`, `{"tool":"bash"}`, `{"tool":"bash","args":{}}`, guardEvents[claude.Name]("ls")},
	}

	for name, list := range events {
		for _, event := range list {
			code, stderr := guardRun(t, name, event)

			assert.Equal(t, 2, code, "%s %s", name, event)
			assert.Contains(t, stderr, "beehest refused the command: the ", "%s %s", name, event)
		}
	}
}

// TestGuardAgent_TheRefusalOfTheCommandIsARefusedCallInTheRecordOfEachAgent feeds the real stderr of the refused command into the stream reader of each agent,
// in the way that agent shows a refused call: the hook error of Claude Code, and the error that the OpenCode plugin throws.
func TestGuardAgent_TheRefusalOfTheCommandIsARefusedCallInTheRecordOfEachAgent(t *testing.T) { //nolint:paralleltest // guardRun replaces os.Stdin of the process
	_, claudeText := guardRun(t, claude.Name, guardEvents[claude.Name]("git add -A"))
	_, openText := guardRun(t, opencode.Name, guardEvents[opencode.Name]("git add -A"))

	hookError, err := json.Marshal("PreToolUse:Bash hook error: [beehest check command]: " + claudeText)
	require.NoError(t, err)

	claudeCall := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_9","name":"Bash","input":{"command":"git add -A"}}]}}`
	claudeResult := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_9","is_error":true,"content":` + string(hookError) + `}]}}`

	adapter := claude.New(nil)
	assert.Equal(t, []string{"git add -A"}, callTexts(adapter.Calls(claudeCall).Shell))
	assert.Equal(t, []string{"toolu_9"}, adapter.Calls(claudeResult).Refused, "Claude Code: the hook error of the command refuses the call")

	fixture, err := os.ReadFile("../../internal/agent/opencode/testdata/refused.jsonl")
	require.NoError(t, err)

	line := strings.Split(string(fixture), "\n")[1]
	thrown, err := json.Marshal("beehest refused: " + strings.TrimSpace(openText))
	require.NoError(t, err)

	const recorded = `"error":"beehest refused: forbidden command: touch of forbidden-ran"`

	require.Contains(t, line, recorded)

	line = strings.Replace(line, recorded, `"error":`+string(thrown), 1)
	seen := opencode.New().Calls(line)

	assert.Equal(t, []string{"call_1"}, seen.Refused, "OpenCode: the error that the plugin throws with the text of the command refuses the call")
	assert.Contains(t, string(thrown), "bulk-staging: git add -A")
}

// callTexts returns the text of each call.
func callTexts(calls []agent.Call) []string {
	texts := make([]string, 0, len(calls))
	for _, call := range calls {
		texts = append(texts, call.Text)
	}

	return texts
}

func TestAdapters_EveryAgentOfTheTableHasAHookReader(t *testing.T) {
	t.Parallel()

	readers := newAdapters(nil, nil, nil).hooks()

	assert.Len(t, readers, len(guardEvents))

	for name := range guardEvents {
		assert.NotNil(t, readers[name], name)
	}
}
