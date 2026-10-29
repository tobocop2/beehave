package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	bashEvent  = `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git add -A"}}`
	cleanEvent = `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."}}`
	readEvent  = `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"command":"git add -A"}}`
)

// hook runs `obee check command` with the event on stdin and returns the exit code and stderr.
func hook(t *testing.T, event string, args ...string) (int, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	root := newRootCommand(t.Context(), BuildInfo{Version: "x"}, map[string]string{}, &stdout, &stderr)
	root.SetIn(strings.NewReader(event))
	root.SetArgs(append([]string{"check", "command"}, args...))

	err := root.ExecuteContext(t.Context())
	if err == nil {
		return 0, stderr.String()
	}

	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Code, stderr.String()
	}

	return -1, err.Error()
}

func TestCheckCommand_RefusesACommandThatBreaksARuleWithExit2AndTheReasonOnStderr(t *testing.T) {
	t.Parallel()

	code, stderr := hook(t, bashEvent)

	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "obee refused the command")
	assert.Contains(t, stderr, "bulk-staging: git add -A")
}

func TestCheckCommand_AllowsACleanCommandAndAnyOtherTool(t *testing.T) {
	t.Parallel()

	for _, event := range []string{cleanEvent, readEvent, `{"tool_name":"Bash"}`} {
		code, stderr := hook(t, event)

		assert.Zero(t, code, event)
		assert.Empty(t, stderr, event)
	}
}

func TestCheckCommand_RefusesOnlyTheKindsListedInForbid(t *testing.T) {
	t.Parallel()

	code, _ := hook(t, bashEvent, "--forbid", "remote-delete,local-delete")
	assert.Zero(t, code)

	code, _ = hook(t, bashEvent, "--forbid", "remote-delete,bulk-staging")
	assert.Equal(t, 2, code)
}

func TestCheckCommand_AnUnknownKindIsAnErrorThatNamesTheKinds(t *testing.T) {
	t.Parallel()

	code, message := hook(t, bashEvent, "--forbid", "bulk")

	assert.Equal(t, 2, code, "a failed hook must block the call")
	assert.Contains(t, message, `unknown kind of command "bulk"`)
	assert.Contains(t, message, "bulk-staging")
}

func TestCheckCommand_AnyOtherFailureOfTheCommandExits2(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"--nope"}, {"--forbid"}, {"extra"}} {
		code, stderr := hook(t, bashEvent, args...)

		assert.Equal(t, 2, code, args)
		assert.Contains(t, stderr, "obee refused the command", args)
	}
}

func TestCheckCommand_AnEventThatCannotBeReadIsRefused(t *testing.T) {
	t.Parallel()

	for _, event := range []string{"", "not json", "null", "[1]"} {
		code, stderr := hook(t, event)

		assert.Equal(t, 2, code, event)
		assert.Contains(t, stderr, "obee refused the command", event)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestRunCheckCommand_AnInputThatFailsToReadIsRefused(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer

	err := runCheckCommand(failingReader{}, &stderr, []string{"bulk-staging"})

	var exit *ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 2, exit.Code)
	assert.Contains(t, stderr.String(), "cannot read the hook event: broken pipe")
}
