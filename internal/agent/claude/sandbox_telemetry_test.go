package claude_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent/claude"
)

// stepShell sources the env file of a run in a POSIX shell with the real go command first on its PATH and an owner HOME, runs the script, and returns
// the empty home folder of the shell and the output of the script. The home folder is where the shell of a sandboxed step keeps what go writes.
func stepShell(t *testing.T, script string) (string, string) {
	t.Helper()

	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go command is not on the PATH")
	}

	runDir, tmp := filepath.Join(t.TempDir(), "run"), t.TempDir()
	require.NoError(t, os.MkdirAll(runDir, 0o700))
	envFile := filepath.Join(runDir, "sandbox-env.sh")
	require.NoError(t, os.WriteFile(envFile, []byte(claude.New(nil).Sandbox(plan(), "", runDir, nil, nil).Files["sandbox-env.sh"]), 0o600))

	shell := exec.CommandContext(t.Context(), "sh", "-c", `. "$1" && `+script, "sh", envFile) //nolint:gosec // a test sources its own temporary file
	shell.Env = []string{
		"PATH=" + filepath.Dir(goBinary) + ":/usr/bin:/bin", "HOME=/Users/owner", "TMPDIR=" + tmp, "GOTOOLCHAIN=local", "XDG_CONFIG_HOME=/Users/owner/.config",
	}
	out, err := shell.CombinedOutput()
	require.NoError(t, err, string(out))

	return filepath.Join(tmp, "beehest-home-run"), strings.TrimSpace(string(out))
}

// The go command starts a telemetry child that it does not wait for, in the home folder of the shell, once it holds the upload token. With the mode
// off it writes no counter and takes no token, so no child leaves the step.
func TestSandbox_TheGoCommandOfTheShellRunsWithTelemetryOffAndLeavesNoCounterOrToken(t *testing.T) {
	t.Parallel()

	home, mode := stepShell(t, `go version >/dev/null && go telemetry`)

	assert.Equal(t, "off", mode, "the go command reads the mode off from the config folder of the home folder of the shell")

	var left []string
	require.NoError(t, filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && !strings.HasSuffix(path, filepath.Join("telemetry", "mode")) {
			left = append(left, path)
		}

		return err
	}))
	assert.Empty(t, left, "no counter file, upload token or other file of go, only the mode files")
}

func TestSandbox_TheConfigFolderOfTheShellIsInItsHomeFolderWhateverTheOwnerSets(t *testing.T) {
	t.Parallel()

	home, config := stepShell(t, `printf %s "$XDG_CONFIG_HOME"`)

	assert.Equal(t, filepath.Join(home, ".config"), config)
}
