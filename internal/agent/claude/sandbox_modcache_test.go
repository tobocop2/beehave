package claude_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/claude"
)

// cacheSettings is the part of the settings that the module cache changes.
type cacheSettings struct {
	Sandbox struct {
		Filesystem struct{ DenyRead, AllowRead, DenyWrite []string }
		Network    struct{ AllowedDomains []string }
	}
	Permissions struct{ Allow, Deny []string }
}

func cacheSettingsOf(t *testing.T, args []string) cacheSettings {
	t.Helper()

	var parsed cacheSettings
	require.NoError(t, json.Unmarshal([]byte(args[len(args)-1]), &parsed))

	return parsed
}

func TestSandbox_TheModuleCacheIsReadableAndNeverWritable(t *testing.T) {
	t.Parallel()

	p := plan()
	p.ModuleCache = "/state/gomodcache/proj"

	launch := claude.New(nil).Sandbox(p, "", "/run", nil, nil)
	got := cacheSettingsOf(t, launch.Args)

	assert.Contains(t, got.Sandbox.Filesystem.AllowRead, "/state/gomodcache/proj")
	assert.Contains(t, got.Sandbox.Filesystem.DenyWrite, "/state/gomodcache/proj")
	assert.Contains(t, got.Permissions.Deny, "Edit(//state/gomodcache/proj/**)")
	assert.NotContains(t, got.Sandbox.Filesystem.DenyRead, "/state/gomodcache/proj")
	assert.Empty(t, got.Permissions.Allow, "no allow rule opens the cache to a write")
	assert.NotContains(t, launch.Args, "/state/gomodcache/proj", "the cache is no working directory of the file tools")
}

func TestSandbox_TheEnvFileSetsTheGoVariablesForAPlanWithAModuleCache(t *testing.T) {
	t.Parallel()

	p := plan()
	p.ModuleCache = "/state/gomodcache/my proj"

	file := claude.New(nil).Sandbox(p, "", "/run", nil, nil).Files["sandbox-env.sh"]

	base := claude.New(nil).Sandbox(plan(), "", "/run", nil, nil).Files["sandbox-env.sh"]
	assert.Equal(t, base+"export GOFLAGS=''\n"+
		"export GOMODCACHE='/state/gomodcache/my proj'\n"+
		"export GOPROXY=off\n"+
		"export GOTOOLCHAIN=local\n", file)

	shell := exec.CommandContext(t.Context(), "sh", "-c", `. "$1" && printf '%s|%s|%s|%s' "$GOMODCACHE" "$GOPROXY" "$GOTOOLCHAIN" "${GOFLAGS-unset}"`, "sh", writeEnv(t, file)) //nolint:gosec // a test sources its own temporary file
	shell.Env = []string{"PATH=/usr/bin:/bin", "TMPDIR=" + t.TempDir(), "GOFLAGS=-mod=mod", "GOPROXY=https://owner.example"}
	out, err := shell.Output()
	require.NoError(t, err)
	assert.Equal(t, "/state/gomodcache/my proj|off|local|", string(out), "the env file wins over the variables of the owner")
}

func writeEnv(t *testing.T, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sandbox-env.sh")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func TestSandbox_APlanWithNoModuleCacheKeepsTheBaseSettingsAndEnvFile(t *testing.T) {
	t.Parallel()

	launch := claude.New(nil).Sandbox(plan(), "", "/run", nil, nil)
	got := cacheSettingsOf(t, launch.Args)

	assert.Equal(t, []string{"/work/tree", "/repo/.git", "/opt/tools", "/run/sandbox-env.sh"}, got.Sandbox.Filesystem.AllowRead)
	assert.Equal(t, []string{"/home/o/.ssh", "/home/o/.aws"}, got.Sandbox.Filesystem.DenyWrite)
	assert.NotContains(t, launch.Files["sandbox-env.sh"], "GO", "the env file names no Go variable")
}

func TestSandbox_TheModuleCacheLeavesTheNetworkListAsItIs(t *testing.T) {
	t.Parallel()

	p := plan()
	p.ModuleCache = "/state/gomodcache/proj"

	with := cacheSettingsOf(t, claude.New(nil).Sandbox(p, "", "/run", nil, nil).Args)
	without := cacheSettingsOf(t, claude.New(nil).Sandbox(plan(), "", "/run", nil, nil).Args)

	assert.Equal(t, []string{"api.example.org"}, with.Sandbox.Network.AllowedDomains)
	assert.Equal(t, without.Sandbox.Network, with.Sandbox.Network)
}
