package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/exectest"
)

// tempScript records the temporary folder that the CLI gets, fills it as the Go command fills its module cache (a read-only folder with a file in it),
// and exits with the code in the file "code" beside it.
const tempScript = `#!/bin/sh
here=$(dirname "$0")
cat > /dev/null
echo "${CLAUDE_CODE_TMPDIR-unset}" > "$here/temp"
if [ -n "$CLAUDE_CODE_TMPDIR" ]; then
  mkdir -p "$CLAUDE_CODE_TMPDIR/claude-1/beehave-home-x/go/pkg/mod/example.com/m@v1"
  echo module > "$CLAUDE_CODE_TMPDIR/claude-1/beehave-home-x/go/pkg/mod/example.com/m@v1/go.mod"
  chmod 0555 "$CLAUDE_CODE_TMPDIR/claude-1/beehave-home-x/go/pkg/mod/example.com/m@v1"
fi
exit "$(cat "$here/code")"
`

// tempFixture returns a fixture whose CLI runs tempScript and exits with code, and the empty folder in which the runner makes the temporary folders.
func tempFixture(t *testing.T, code string) (*fixture, string) {
	t.Helper()

	f := newFixture(t, "claude", claude.Name, "")
	require.NoError(t, exectest.Write(filepath.Join(f.records, "claude"), tempScript))
	require.NoError(t, os.WriteFile(filepath.Join(f.records, "code"), []byte(code), 0o600))

	return f, t.TempDir()
}

func TestRunner_TheTemporaryFolderOfASandboxedRunIsGoneAfterTheRunWhateverItsExit(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	for name, code := range map[string]int{"success": 0, "failure": 3} {
		f, root := tempFixture(t, strconv.Itoa(code))
		f.spec.Sandbox = &agent.SandboxPlan{Work: f.work}

		got := agent.Runner{Version: seen("2.1.330 (Claude Code)\n"), Temp: root}.Run(context.Background(), f.spec)

		require.Equal(t, agent.OutcomeExited, got.Outcome, name+": "+got.Note)
		assert.Equal(t, code, got.ExitCode, name)

		temp := record(t, f.records, "temp")
		assert.Equal(t, root, filepath.Dir(temp), name+": the CLI gets a folder that the runner made in its root")
		assert.Regexp(t, `^bh-[0-9]+$`, filepath.Base(temp), name)

		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		assert.Empty(t, entries, name+": the runner removes the folder and its read-only content when the run ends")
	}
}

func TestRunner_ARunWithNoSandboxGetsNoTemporaryFolderOfTheRunner(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f, root := tempFixture(t, "0")

	got := agent.Runner{Temp: root}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)
	assert.Equal(t, "unset", record(t, f.records, "temp"), "the CLI of a run with no sandbox gets no CLAUDE_CODE_TMPDIR")

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries, "the runner makes no folder for a run with no sandbox")
}

func TestRunner_ASandboxedRunWhoseTemporaryFolderCannotBeMadeDoesNotStartTheCLI(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f, root := tempFixture(t, "0")
	f.spec.Sandbox = &agent.SandboxPlan{Work: f.work}
	missing := filepath.Join(root, "missing")

	got := agent.Runner{Version: seen("2.1.330 (Claude Code)\n"), Temp: missing}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "cannot make the temporary folder for claude in "+missing)
	assert.NoFileExists(t, filepath.Join(f.records, "temp"))
}
