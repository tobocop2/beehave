package claude_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/agent/claude"
	"github.com/tobocop2/obee/internal/exectest"
	"github.com/tobocop2/obee/internal/model"
)

// fakeClaude does what Claude Code does with a PreToolUse hook. It reads the hook command and the matcher from the JSON after --settings,
// takes the prompt on stdin as the Bash command of one tool call, starts the hook command with the event as JSON on stdin,
// and prints a result line that holds the matcher, the exit code and the stderr of the hook.
const fakeClaude = `#!/bin/sh
settings=
while [ $# -gt 0 ]; do
  if [ "$1" = --settings ]; then settings=$2; fi
  shift
done
read -r prompt
hook=${settings#*\"command\":\"}
hook=${hook%%\"*}
matcher=${settings#*\"matcher\":\"}
matcher=${matcher%%\"*}
event=$(printf '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"%s"}}' "$prompt")
err=$(printf '%s' "$event" | "$hook" 2>&1 >/dev/null)
code=$?
printf '{"type":"result","session_id":"s1","result":"%s command exit=%s %s"}\n' "$matcher" "$code" "$err"
`

// The guard script reads the event the way a hook script does and refuses a command that holds "add -A" with exit 2.
const guardScript = `#!/bin/sh
event=$(cat)
case "$event" in
  *'"hook_event_name":"PreToolUse"'*'"tool_name":"Bash"'*) ;;
  *) exit 0 ;;
esac
case "$event" in
  *'add -A'*) echo "bulk staging is refused" >&2; exit 2 ;;
esac
exit 0
`

// guardOutcome runs the real adapter with the guard script against the fake claude and returns the text of its result line.
func guardOutcome(t *testing.T, bashCommand string) string {
	t.Helper()

	dir := t.TempDir()
	guard := filepath.Join(dir, "guard.sh")
	require.NoError(t, exectest.Write(guard, guardScript))
	require.NoError(t, exectest.Write(filepath.Join(dir, "claude"), fakeClaude))

	var out bytes.Buffer

	result := agent.Runner{}.Run(context.Background(), agent.Spec{
		Adapter: claude.New(), Runner: model.Runner{Name: "r", Agent: model.AgentClaude, ConfigDir: dir},
		Dir: dir, Prompt: bashCommand + "\n", Env: map[string]string{"PATH": dir + ":/bin:/usr/bin"}, Log: &out, Guard: guard,
	})
	require.Equal(t, agent.OutcomeExited, result.Outcome, result.Note)
	require.Zero(t, result.ExitCode, result.Stderr)

	_, text := claude.New().Line(strings.TrimSpace(out.String()))

	return text
}

func TestGuard_TheHookRefusesACommandBeforeTheCallWithExit2(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Bash command exit=2 bulk staging is refused", guardOutcome(t, "git add -A"))
}

func TestGuard_TheHookLetsACleanCommandThrough(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Bash command exit=0 ", guardOutcome(t, "go test ./..."))
}

func TestGuard_AddsOneBashHookWithTheSettingsFlag(t *testing.T) {
	t.Parallel()

	launch := claude.New().Guard("/bin/obee check command", "/run")
	got := launch.Args

	assert.Empty(t, launch.Env)
	assert.Empty(t, launch.Files)
	require.Len(t, got, 2)
	assert.Equal(t, "--settings", got[0])
	assert.JSONEq(t, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/obee check command","onFailure":"block"}]}]}}`, got[1])
}
