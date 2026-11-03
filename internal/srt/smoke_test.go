//go:build smoke

package srt_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/srt"
)

const (
	listedDomain   = "example.com"
	unlistedDomain = "example.org"
	smokeTimeout   = time.Minute
	secretText     = "obee-smoke-secret"
)

// runSmoke runs command through the real srt with the settings file that srt.Wrap writes, and returns the output and the exit code.
func runSmoke(t *testing.T, plan agent.SandboxPlan, home, command string) (string, int) {
	t.Helper()

	args, cleanup, err := srt.Wrap(plan, home, command)
	require.NoError(t, err)

	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), smokeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, srt.Program, args...)
	cmd.Dir = plan.Work
	out, err := cmd.CombinedOutput()

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		t.Logf("%s => exit %d: %s", command, exit.ExitCode(), out)

		return string(out), exit.ExitCode()
	}

	require.NoError(t, err)
	t.Logf("%s => exit 0: %s", command, out)

	return string(out), 0
}

// TestSmoke_TheSettingsFileThatObeeWritesRunsThroughARealSrt is run by `make smoke-gate-sandbox`. It needs srt, its programs and the network.
func TestSmoke_TheSettingsFileThatObeeWritesRunsThroughARealSrt(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	work, err := os.MkdirTemp(home, "obee-smoke-work-")
	require.NoError(t, err)

	secret := filepath.Join(home, filepath.Base(work)+"-secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte(secretText), 0o600))

	t.Cleanup(func() {
		assert.NoError(t, os.RemoveAll(work))
		assert.NoError(t, os.Remove(secret))
	})

	require.NoError(t, os.WriteFile(filepath.Join(work, "in.txt"), []byte("control"), 0o600))

	plan := agent.SandboxPlan{Work: work, Network: []string{listedDomain}}
	fetch := func(domain string) string {
		return "curl -s -o /dev/null -w '%{http_code}' --max-time 20 https://" + domain
	}

	// Without a read, the secret in the home folder exists but the command cannot see it.
	require.FileExists(t, secret)

	out, code := runSmoke(t, plan, home, "cat "+secret)
	assert.NotZero(t, code, "a read under the home folder is refused")
	assert.NotContains(t, out, secretText)

	out, code = runSmoke(t, plan, home, "cat in.txt")
	assert.Zero(t, code, "the work folder under the home folder stays readable")
	assert.Contains(t, out, "control")

	out, code = runSmoke(t, plan, home, fetch(unlistedDomain))
	assert.True(t, code != 0 || out == "000" || out == "403", "a domain that is not listed is refused, got exit %d and %q", code, out)

	out, code = runSmoke(t, plan, home, fetch(listedDomain))
	assert.Zero(t, code, "a listed domain works")
	assert.Equal(t, "200", out)

	_, code = runSmoke(t, plan, home, "exit 7")
	assert.Equal(t, 7, code, "the exit code of the command passes through srt")
}
