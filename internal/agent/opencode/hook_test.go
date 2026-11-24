package opencode_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/opencode"
)

func TestDecodeHook_ReadsTheCallThatThePluginForwards(t *testing.T) {
	t.Parallel()

	call, err := opencode.DecodeHook(`{"directory":"/work","tool":"bash","args":{"command":"git add -A","description":"stage"}}`)

	require.NoError(t, err)
	assert.Equal(t, agent.HookCall{Dir: "/work", Command: "git add -A"}, call)
}

func TestDecodeHook_ReadsTheShellCallOfOpenCode2x(t *testing.T) {
	t.Parallel()

	call, err := opencode.DecodeHook(`{"directory":"/work","tool":"shell","args":{"command":"git add -A"}}`)

	require.NoError(t, err)
	assert.Equal(t, agent.HookCall{Dir: "/work", Command: "git add -A"}, call)
}

func TestDecodeHook_ACallOfAnotherToolIsAnError(t *testing.T) {
	t.Parallel()

	_, err := opencode.DecodeHook(`{"directory":"/work","tool":"read","args":{"filePath":"/x"}}`)

	require.ErrorContains(t, err, "not a call of the shell tool")
}

func TestDecodeHook_AnInputOfAnotherFormatIsAnError(t *testing.T) {
	t.Parallel()

	_, err := opencode.DecodeHook(`{"tool_name":"Bash","tool_input":{"command":"git add -A"}}`)

	require.ErrorContains(t, err, "not a call of the shell tool", "the format of Claude Code names no OpenCode tool")
}

func TestDecodeHook_ABashCallWithNoCommandIsAnError(t *testing.T) {
	t.Parallel()

	for _, input := range []string{`{"tool":"bash"}`, `{"tool":"bash","args":{}}`, `{"tool":"bash","args":null}`, `{"tool":"bash","args":{"command":""}}`} {
		_, err := opencode.DecodeHook(input)

		require.ErrorContains(t, err, "has no command", input)
	}
}

func TestDecodeHook_AnInputThatIsNotTheFormatIsAnErrorThatNamesTheEvent(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "not json", "null", "[1]", `{"tool":1}`, `{"tool":"bash","args":{"command":1}}`} {
		_, err := opencode.DecodeHook(input)

		require.Error(t, err, input)
		assert.Contains(t, err.Error(), "the hook event", input)
	}
}
