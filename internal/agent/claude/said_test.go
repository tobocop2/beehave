package claude_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/agent/claude"
)

func TestCalls_AnAssistantLineSaysItsTextsAndToolCallsInOrderWithTheTargetOfEachCall(t *testing.T) {
	t.Parallel()

	line := assistant(t,
		map[string]any{"type": "text", "text": "Looking."},
		map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "ls"}},
		map[string]any{"type": "tool_use", "id": "t2", "name": "Read", "input": map[string]any{"file_path": "a.go"}},
		map[string]any{"type": "tool_use", "id": "t3", "name": "TodoWrite", "input": map[string]any{"todos": 1}},
		map[string]any{"type": "text", "text": ""},
		"not an object",
	)

	assert.Equal(t, []agent.Said{{Text: "Looking."}, {Call: true, Text: "Bash: ls"}, {Call: true, Text: "Read: a.go"}, {Call: true, Text: "TodoWrite"}}, claude.New(nil).Calls(line).Said)
}

func TestCalls_OnlyAnAssistantLineSays(t *testing.T) {
	t.Parallel()

	adapter := claude.New(nil)

	assert.Empty(t, adapter.Calls(toolResult(t, "t1", false, "ok")).Said)
	assert.Empty(t, adapter.Calls(toJSON(t, map[string]any{"type": "result", "result": "the last message"})).Said, "the result line repeats the last message, so it says nothing")
}
