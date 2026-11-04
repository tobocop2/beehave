package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/sandbox/landlock"
)

// realProgramGates returns a program gate runner that uses the real Landlock enforcer and the real home folder, with the domains as the gate_network list.
func realProgramGates(t *testing.T, folder string, domains ...string) programs {
	t.Helper()

	if err := landlock.New().Check(); err != nil {
		t.Skipf("Landlock cannot run on this machine: %v", err)
	}

	if _, err := os.Stat("/usr/bin/curl"); err != nil {
		t.Skipf("curl is missing: %v", err)
	}

	box := sandboxFor(t, landlock.New(), nil)
	box.home = os.UserHomeDir
	project := box.projects["p"]
	project.GateNetwork, project.GateAllowWrite = domains, []string{"cache"}
	box.projects["p"] = project

	return programs{trees: treeAt(folder), sandbox: box}
}

// secretInHome writes a file with a marker under the real home folder and removes it at the end.
func secretInHome(t *testing.T) string {
	t.Helper()

	home, err := os.UserHomeDir()
	require.NoError(t, err)

	dir, err := os.MkdirTemp(home, "beehave-program-gate-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })

	secret := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("hunter2"), 0o600))

	return secret
}

// pong starts a local server that answers pong and returns its URL, and the same URL with the name localhost, which the gate_network list does not name.
func pong(t *testing.T) (listed, unlisted string) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "pong") }))
	t.Cleanup(server.Close)

	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)

	return server.URL, "http://localhost:" + port
}

func gateLine(t *testing.T, runs programs, line string) (string, int) {
	t.Helper()

	got, err := runs.Run(t.Context(), flow.CommandRequest{
		Project: "p", Item: "1", Gate: true, Run: []string{"sh", "-c", line},
		Env: []string{"PATH=/usr/bin:/bin"},
	})
	require.NoError(t, err)

	return got.Stdout, got.RC
}

// TestPrograms_AProgramGateUnderTheRealEnforcerCannotReadTheHomeFolderAndReachesOnlyTheAllowedDomains runs a program gate of a project with the sandbox on
// under Landlock, in a plain folder (no repository) and in a git work tree.
func TestPrograms_AProgramGateUnderTheRealEnforcerCannotReadTheHomeFolderAndReachesOnlyTheAllowedDomains(t *testing.T) {
	t.Parallel()

	secret := secretInHome(t)
	listed, unlisted := pong(t)

	for name, folder := range map[string]string{"a plain folder": t.TempDir(), "a git work tree": repoWithCommit(t)} {
		runs := realProgramGates(t, folder, "127.0.0.1")

		out, code := gateLine(t, runs, "cat "+secret)
		assert.NotZero(t, code, "%s: the home folder is closed: %s", name, out)
		assert.NotContains(t, out, "hunter2", name)

		out, code = gateLine(t, runs, "curl --fail --silent --max-time 20 "+listed)
		assert.Zero(t, code, "%s: the listed domain is open: %s", name, out)
		assert.Contains(t, out, "pong", name)

		out, code = gateLine(t, runs, "curl --fail --silent --max-time 20 "+unlisted)
		assert.NotZero(t, code, "%s: a domain that is not listed stays closed: %s", name, out)
		assert.NotContains(t, out, "pong", name)

		out, code = gateLine(t, runs, "curl --fail --silent --max-time 20 --noproxy '*' "+listed)
		assert.NotZero(t, code, "%s: a connection that skips the proxy stays closed: %s", name, out)
		assert.NotContains(t, out, "pong", name)
	}
}

// TestPrograms_AProgramGateInAPlainFolderCannotWriteItsHooksOrItsFilesButWritesAGateAllowWritePath proves, under the real enforcer, the rule for a folder
// with no copy: the gate reads the folder, writes only a gate_allow_write path and the temporary folder, and plants no hook folder.
func TestPrograms_AProgramGateInAPlainFolderCannotWriteItsHooksOrItsFilesButWritesAGateAllowWritePath(t *testing.T) {
	t.Parallel()

	folder := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(folder, "input"), []byte("data"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(folder, "cache"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(folder, ".husky"), 0o700))

	runs := realProgramGates(t, folder)

	out, code := gateLine(t, runs, "cat input")
	assert.Zero(t, code, out)
	assert.Contains(t, out, "data")

	for name, line := range map[string]string{
		"a new file":         "echo x > new",
		"a changed file":     "echo x > input",
		"a hook":             "echo x > .husky/pre-commit",
		"a new settings":     "mkdir .claude && echo {} > .claude/settings.json",
		"a removed file":     "rm input",
		"a removed hook dir": "rmdir .husky",
	} {
		out, code = gateLine(t, runs, line)
		assert.NotZero(t, code, "%s must fail: %s", name, out)
	}

	out, code = gateLine(t, runs, "echo x > cache/out && echo x > $TMPDIR/out")
	assert.Zero(t, code, out)

	entries, err := os.ReadDir(folder)
	require.NoError(t, err)

	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	assert.ElementsMatch(t, []string{"input", "cache", ".husky"}, names)
	assert.FileExists(t, filepath.Join(folder, "cache", "out"))
	assert.NoFileExists(t, filepath.Join(folder, ".husky", "pre-commit"))
}
