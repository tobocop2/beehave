package opencode_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/opencode"
	"github.com/tobocop2/beehave/internal/exectest"
)

// driver loads the plugin like OpenCode does, calls the hook of one tool call and prints the result on one line.
const driver = `import { BeehaveGuard } from "./plugin.mjs"
const hooks = await BeehaveGuard({ directory: process.cwd() })
try {
  await hooks["tool.execute.before"]({ tool: process.argv[2] }, { args: { command: "ls" } })
  console.log("pass")
} catch (err) {
  console.log("refused: " + err.message)
}
`

// nodeRun runs the plugin of a guard command on Node, for a tool call of the given tool, and returns what the hook did and whether the marker exists.
func nodeRun(t *testing.T, node, guard, tool string) (outcome string, marker bool) {
	t.Helper()

	dir := t.TempDir()
	loaded := filepath.Join(dir, "loaded")
	text := strings.Replace(opencode.New().Guard(guard, dir, nil, nil).Files[pluginPath], `"`+filepath.Join(dir, "config/beehave-guard-loaded")+`"`, `"`+loaded+`"`, 1)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.mjs"), []byte(text), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "driver.mjs"), []byte(driver), 0o600))

	out, err := exec.Command(node, filepath.Join(dir, "driver.mjs"), tool).CombinedOutput() //nolint:gosec,noctx // a fixed program on a temporary folder
	require.NoError(t, err, string(out))

	_, statErr := os.Stat(loaded)

	return strings.TrimSpace(string(out)), statErr == nil
}

// stopper is the part of a test that nodeAvailable ends it with.
type stopper interface {
	Helper()
	Fatal(args ...any)
	Skip(args ...any)
}

// nodeAvailable says whether the test can run the plugin. A missing Node skips the test, except where the CI variable is set: the gate counts this test as proof
// of the plugin, so there it fails and cannot pass unrun.
func nodeAvailable(t stopper, lookup error, ci string) bool {
	t.Helper()

	if lookup == nil {
		return true
	}

	if ci != "" {
		t.Fatal("Node is not installed, and this test is a proof that the gate needs when the CI variable is set")

		return false
	}

	t.Skip("Node is not installed; TestGuard_EveryDecisionOfThePluginIsInIt reads the same decisions as text")

	return false
}

// recordedStop is a stopper that records how the test would end.
type recordedStop struct{ fatal, skip bool }

func (*recordedStop) Helper() {}

func (s *recordedStop) Fatal(...any) { s.fatal = true }

func (s *recordedStop) Skip(...any) { s.skip = true }

func TestGuard_ANodeThatIsMissingFailsTheNodeTestWhereCIIsSetAndSkipsItElsewhere(t *testing.T) {
	t.Parallel()

	missing := errors.New("executable file not found")

	cases := []struct {
		name      string
		lookup    error
		ci        string
		available bool
		want      recordedStop
	}{
		{"node found", nil, "", true, recordedStop{}},
		{"node found in CI", nil, "true", true, recordedStop{}},
		{"node missing in CI", missing, "true", false, recordedStop{fatal: true}},
		{"node missing elsewhere", missing, "", false, recordedStop{skip: true}},
	}

	for _, tc := range cases {
		var got recordedStop

		assert.Equal(t, tc.available, nodeAvailable(&got, tc.lookup, tc.ci), tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}
}

// checkScript writes a guard command that prints stderr and exits with code.
func checkScript(t *testing.T, stderr, code string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "check")
	require.NoError(t, exectest.Write(path, "#!/bin/sh\ncat > /dev/null\necho '"+stderr+"' >&2\nexit "+code+"\n"))

	return path
}

func TestGuard_ThePluginRunOnNodeDecidesFromTheExitCodeOfTheCheck(t *testing.T) {
	t.Parallel()

	node, err := exec.LookPath("node")
	if !nodeAvailable(t, err, os.Getenv("CI")) { //nolint:forbidigo // the CI variable of the job decides whether a missing Node fails this proof test
		return
	}

	cases := []struct {
		name, code, tool, want string
	}{
		{"exit code 0 lets a bash call through", "0", "bash", "pass"},
		{"exit code 2 refuses with the text of the check", "2", "bash", "refused: beehave refused: no staging"},
		{"another exit code refuses", "1", "bash", "refused: beehave refused: the check of the command failed"},
		{"a call of another tool is never checked", "2", "read", "pass"},
	}

	for _, tc := range cases {
		outcome, marker := nodeRun(t, node, checkScript(t, "no staging", tc.code), tc.tool)

		assert.Equal(t, tc.want, outcome, tc.name)
		assert.True(t, marker, tc.name+": the plugin writes its marker when it loads")
	}
}
