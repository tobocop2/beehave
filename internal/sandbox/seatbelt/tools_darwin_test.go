//go:build darwin

package seatbelt

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/sandbox"
)

// workPlan reads the system folders and writes one new folder, and returns the plan, the folder and an environment that keeps every cache in it.
func workPlan(t *testing.T, read ...string) (plan sandbox.Plan, dir string, env []string) {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	plan = sandbox.Plan{Read: append(sandbox.SystemRead(), read...), Write: append([]string{dir}, sandbox.SystemWrite()...)}
	env = []string{"PATH=/opt/homebrew/bin:/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "GOCACHE=" + filepath.Join(dir, "gocache"), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local"}

	return plan, dir, env
}

func TestRun_AnAppleDeveloperToolShimIsRefusedWithTheReason(t *testing.T) {
	t.Parallel()

	plan, dir, env := workPlan(t)

	var stderr bytes.Buffer

	cmd := command(env, "/usr/bin/git", "status")
	cmd.Dir, cmd.Stderr = dir, &stderr

	code, err := New().Run(t.Context(), plan, 0, cmd)

	require.ErrorContains(t, err, "/usr/bin/git")
	assert.Contains(t, err.Error(), "Apple developer-tools shim")
	assert.Zero(t, code)
	assert.Empty(t, stderr.String(), "the shim ran no code")
}

func TestRun_GitFromOutsideUsrBinRunsStatusUnderThePlan(t *testing.T) {
	t.Parallel()

	git, err := exec.LookPath("/opt/homebrew/bin/git")
	if err != nil {
		t.Skip("no git outside /usr/bin on this machine")
	}

	plan, dir, env := workPlan(t)
	require.NoError(t, exec.CommandContext(t.Context(), git, "init", "-q", dir).Run()) //nolint:gosec // the path is the fixed Homebrew one

	var out bytes.Buffer

	cmd := command(env, git, "status", "--short")
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &out, &out

	code, err := New().Run(t.Context(), plan, 0, cmd)

	require.NoError(t, err)
	assert.Zero(t, code, out.String())
}

func TestRun_GoBuildRunsUnderThePlan(t *testing.T) {
	t.Parallel()

	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on this machine")
	}

	root, err := exec.CommandContext(t.Context(), goTool, "env", "GOROOT").Output() //nolint:gosec // the go found on PATH is the one the test builds with
	require.NoError(t, err)

	plan, base, env := workPlan(t, string(bytes.TrimSpace(root)))

	// Go ignores a go.mod in the system temporary folder itself, so the module lies one folder below it.
	dir := filepath.Join(base, "module")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module probe\n\ngo 1.21\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600))

	var out bytes.Buffer

	cmd := command(env, goTool, "build", "-o", filepath.Join(dir, "probe"), ".")
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &out, &out
	cmd.Env = append(cmd.Env, "PATH="+filepath.Dir(goTool)+":/usr/bin:/bin")

	code, err := New().Run(t.Context(), plan, 0, cmd)

	require.NoError(t, err)
	assert.Zero(t, code, out.String())
	assert.FileExists(t, filepath.Join(dir, "probe"))
}

func TestRun_ALaunchAgentAndAScriptingBridgeLeaveNoFileOutsideThePlan(t *testing.T) {
	t.Parallel()

	plan, dir, env := workPlan(t)

	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	for _, line := range []string{
		"launchctl submit -l beehave.probe.submit -- /usr/bin/touch " + filepath.Join(outside, "from-launchctl"),
		"osascript -e 'do shell script \"/usr/bin/touch " + filepath.Join(outside, "from-osascript") + "\"'",
	} {
		cmd := command(env, "sh", "-c", line+" || true")
		cmd.Dir = dir

		_, err := New().Run(t.Context(), plan, 0, cmd)
		require.NoError(t, err)
	}

	// A submitted job starts later, so give launchd a bounded moment before the check.
	time.Sleep(2 * time.Second)
	// The job may not exist, and then remove fails and there is nothing to remove.
	if err := exec.CommandContext(t.Context(), "/bin/launchctl", "remove", "beehave.probe.submit").Run(); err != nil {
		t.Logf("launchctl remove: %v", err)
	}

	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "no file appeared outside the plan")
}
