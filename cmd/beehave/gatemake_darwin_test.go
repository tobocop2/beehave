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

// A sandboxed gate runs make under Seatbelt: the plan opens the developer folder that the engine found at start, so the shim of make finds the real make.
func TestGateSandbox_MakeRunsUnderTheRealEnforcer(t *testing.T) {
	t.Parallel()

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

	out, code := box.cloneOf(dir).run(t.Context(), "p", dir, []string{"sh", "-c", "make check"}, nil, 0)

	assert.Zero(t, code, out)
	assert.Contains(t, out, "make-ran")
	assert.NotContains(t, out, "libxcrun")
}
