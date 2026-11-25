package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/sandbox"
	"github.com/tobocop2/beehave/internal/sandbox/seatbelt"
)

// makeGate runs the shell command in a sandboxed gate under the real Seatbelt enforcer, in a repository whose Makefile has one target, check. It skips
// with its reason when this machine has no Seatbelt or no make behind xcrun.
func makeGate(t *testing.T, command string) (string, int) {
	t.Helper()

	enforcer := seatbelt.New()
	if err := enforcer.Check(); err != nil {
		t.Skipf("no Seatbelt enforcer here: %v", err)
	}

	if err := exec.CommandContext(t.Context(), "xcrun", "-f", "make").Run(); err != nil {
		t.Skipf("xcrun finds no make here: %v", err)
	}

	box := newGateSandbox(sandboxedProject(model.SandboxOn, config.LocalHost), cloneLock{repos: t.TempDir()}, nil)
	box.enforcer = func(string) (sandbox.Enforcer, error) { return enforcer, nil }
	box.home = func() (string, error) { return t.TempDir(), nil }

	require.NotEmpty(t, box.developer, "xcrun finds make, so xcode-select names the developer folder")

	dir := repoWithCommit(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("check:\n\t@echo make-ran\n"), 0o600))
	runGit(t, dir, "add", "Makefile")
	runGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "make")

	return box.cloneOf(dir).run(t.Context(), "p", dir, []string{"sh", "-c", command}, nil, 0)
}

// A sandboxed gate runs make under Seatbelt: the plan opens the developer folder that the engine found at start, so the shim of make finds the real make.
func TestGateSandbox_MakeRunsUnderTheRealEnforcer(t *testing.T) {
	t.Parallel()

	out, code := makeGate(t, "make check")

	assert.Zero(t, code, out)
	assert.Contains(t, out, "make-ran")
	assert.NotContains(t, out, "libxcrun")
}

// The cache of xcrun is a new file in the temporary folder of the command, which is a cold cache: make runs, and the file xcrun_db names exists, has
// content and lies below TMPDIR. Without the env pair xcrun_db is unset, and the check of the file fails.
func TestGateSandbox_MakeKeepsTheXcrunCacheInTheTemporaryFolderOfTheCommand(t *testing.T) {
	t.Parallel()

	out, code := makeGate(t, `make check && test -s "$xcrun_db" && case "$xcrun_db" in "$TMPDIR"/*) ;; *) exit 9 ;; esac`)

	assert.Zero(t, code, out)
	assert.Contains(t, out, "make-ran")
}
