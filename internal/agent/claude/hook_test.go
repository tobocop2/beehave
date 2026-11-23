package claude_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
)

func TestDecodeHook_ReadsThePreToolUseEventOfABashCall(t *testing.T) {
	t.Parallel()

	call, err := claude.DecodeHook(`{"hook_event_name":"PreToolUse","cwd":"/work","tool_name":"Bash","tool_input":{"command":"git add -A"}}`)

	require.NoError(t, err)
	assert.Equal(t, agent.HookCall{Shell: true, Dir: "/work", Command: "git add -A"}, call)
}

func TestDecodeHook_ACallOfAnotherToolIsNoShellCall(t *testing.T) {
	t.Parallel()

	call, err := claude.DecodeHook(`{"tool_name":"Read","tool_input":{"file_path":"/x"}}`)

	require.NoError(t, err)
	assert.False(t, call.Shell)
}

func TestDecodeHook_AnInputOfAnotherFormatIsNoShellCall(t *testing.T) {
	t.Parallel()

	call, err := claude.DecodeHook(`{"directory":"/work","tool":"bash","args":{"command":"git add -A"}}`)

	require.NoError(t, err)
	assert.False(t, call.Shell, "the format of OpenCode names no Claude Code tool")
}

func TestDecodeHook_AnInputThatIsNotTheFormatIsAnErrorThatNamesTheEvent(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "not json", "null", "[1]", `{"tool_name":1}`, `{"tool_name":"Bash","tool_input":{"command":1}}`} {
		_, err := claude.DecodeHook(input)

		require.Error(t, err, input)
		assert.Contains(t, err.Error(), "the hook event", input)
	}
}
