package host_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
)

func TestSlotWrap_ReturnsTheCommandAsItIsForAJobWithNoSlot(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "echo hi", host.SlotWrap(model.Slot{}, "'/x'", "'/m'", "echo hi"))
}

func TestSlotWrap_PutsTheCountTheClassAndTheCommandInTheScript(t *testing.T) {
	t.Parallel()

	script := host.SlotWrap(model.Slot{Class: "bench", Count: 4}, "/x/slots/bench", "/x/u.waiting", "echo hi")

	assert.Contains(t, script, `-le 4`)
	assert.Contains(t, script, `class bench on this host`)
	assert.Contains(t, script, `bh_d=/x/slots/bench;`)
	assert.Contains(t, script, `bh_m=/x/u.waiting;`)
	assert.Contains(t, script, "sleep 2;")
	assert.True(t, len(script) > len("echo hi") && script[len(script)-len("echo hi"):] == "echo hi")
}

func TestSlotWrap_TheScriptRunsTheCommandAndKeepsItsExitStatusWhenASlotIsFree(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock is not on PATH")
	}

	dir := t.TempDir()
	script := host.SlotWrap(model.Slot{Class: "bench", Count: 1}, model.ShellQuote(filepath.Join(dir, "slots", "bench")), model.ShellQuote(filepath.Join(dir, "m")), "echo ran > "+model.ShellQuote(filepath.Join(dir, "out"))+"; exit 7")

	err := exec.Command("sh", "-c", script).Run()

	var exit *exec.ExitError

	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 7, exit.ExitCode())

	out, readErr := os.ReadFile(filepath.Join(dir, "out"))
	require.NoError(t, readErr)
	assert.Equal(t, "ran\n", string(out))
	assert.NoFileExists(t, filepath.Join(dir, "m"), "a job that took a slot at once never shows the marker")
}
