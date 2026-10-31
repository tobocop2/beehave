package gate_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/exectest"
	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
)

// pinned runs a local gate in a folder that holds the pin file text. Each tool is a fake program with the given body, found first on the path of a real sh.
// The gate command is `make check`; the fake tool `make` records that it ran. It returns the result and whether the gate command ran.
func pinned(t *testing.T, pins string, tools map[string]string, testReport string) (gate.Result, bool) {
	t.Helper()

	result, ran, _ := pinnedAt(t, pins, tools, testReport)

	return result, ran
}

// treePins is what the lane's own copy of the pin file says. The gate must ignore it: only the pin file at the base commit counts.
const treePins = "gofake 0.0.1\n"

// pinnedAt is pinned that also returns the commands that the gate started. The fake shell answers `git show` with pins, the text of the pin file at the base commit.
func pinnedAt(t *testing.T, pins string, tools map[string]string, testReport string) (gate.Result, bool, []string) {
	t.Helper()

	dir, bin := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".tool-versions"), []byte(treePins), 0o600))

	var commands []string

	for name, body := range tools {
		require.NoError(t, exectest.Write(filepath.Join(bin, name), "#!/bin/sh\n"+body+"\n"))
	}

	require.NoError(t, exectest.Write(filepath.Join(bin, "make"), "#!/bin/sh\ntouch ran\n"))

	shell := func(ctx context.Context, dir, command string) (string, int) {
		commands = append(commands, command)

		if strings.HasPrefix(command, "git show ") {
			if pins == absentAtBase {
				return "fatal: path does not exist\n", 128
			}

			return pins, 0
		}

		c := exec.CommandContext(ctx, "/bin/sh", "-c", command) //nolint:gosec // the test runs its own command line
		c.Dir = dir
		c.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}

		out, err := c.CombinedOutput()
		if err != nil {
			return string(out), c.ProcessState.ExitCode()
		}

		return string(out), 0
	}

	tree := &fakeTree{states: []gate.State{clean(headA), clean(headA)}}
	g := gate.New(shell, tree.read, nil)
	p := model.Project{Name: "demo", Gate: "make check", BuildHost: "local", BaseBranch: "main", PinFile: ".tool-versions", TestReport: testReport}

	result, err := g.Run(context.Background(), gate.Request{Project: p, Dir: dir})
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(dir, "ran"))

	return result, statErr == nil, commands
}

// absentAtBase makes the fake shell report that the base commit has no pin file.
const absentAtBase = "\x00absent"

func TestPins_ThePinFileIsReadFromTheBaseCommitAndNotFromTheTree(t *testing.T) {
	t.Parallel()

	result, ran, commands := pinnedAt(t, "gofake 1.27.3\n", map[string]string{"gofake": `echo "gofake version go1.26.0 linux/amd64"`}, "")

	assert.False(t, ran)
	assert.False(t, result.Passed)
	assert.Equal(t, "git show origin/main:.tool-versions", commands[0])
	assert.Contains(t, result.Report, "pins version 1.27.3")
}

func TestPins_APinFileThatIsAbsentAtTheBaseCommitVoidsTheStep(t *testing.T) {
	t.Parallel()

	result, ran := pinned(t, absentAtBase, map[string]string{"gofake": `echo "gofake version go0.0.1"`}, "")

	assert.False(t, ran)
	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusUnknown)}, result.Steps)
	assert.Contains(t, result.Report, ".tool-versions")
}

func TestPins_AVersionMustBeEqualToThePinAndNotContainTheNumberOfTheOtherPart(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct{ pin, says string }{
		"pin 1.2 is not inside go1.27.3":   {"1.2", "gofake version go1.27.3 linux/amd64"},
		"pin 1.27 is not a part of 1.27.3": {"1.27", "gofake 1.27.3"},
		"pin 1.27.3 is not inside 11.27.3": {"1.27.3", "gofake 11.27.3"},
	} {
		result, ran := pinned(t, "gofake "+c.pin+"\n", map[string]string{"gofake": "echo '" + c.says + "'"}, "")

		assert.False(t, ran, name)
		assert.False(t, result.Passed, name)
	}
}

func TestPins_AToolAtAnotherVersionVoidsTheStepAndTheGateDoesNotRun(t *testing.T) {
	t.Parallel()

	result, ran := pinned(t, "gofake 1.27.3\n", map[string]string{"gofake": `echo "gofake version go1.26.0 linux/amd64"`}, "")

	assert.False(t, ran)
	assert.False(t, result.Passed)
	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusUnknown)}, result.Steps)
	assert.Contains(t, result.Report, "gofake")
	assert.Contains(t, result.Report, "pins version 1.27.3")
	assert.Contains(t, result.Report, "go1.26.0")
}

func TestPins_AToolAtThePinnedVersionLetsTheGateRunAndPass(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"version subcommand": `echo "gofake version gofake1.27.3 linux/amd64"`,
		"flag only":          `[ "$1" = "--version" ] && echo "gofake v1.27.3" || exit 2`,
	} {
		result, ran := pinned(t, "# the tools\ngofake 1.27.3 # pinned\n", map[string]string{"gofake": body}, "")

		assert.True(t, ran, name)
		assert.True(t, result.Passed, name)
		assert.Equal(t, passed, result.Steps, name)
	}
}

func TestPins_AToolThatCannotBeReadVoidsTheStep(t *testing.T) {
	t.Parallel()

	for name, tools := range map[string]map[string]string{
		"tool missing":          {},
		"tool exits non-zero":   {"gofake": "exit 3"},
		"tool prints no number": {"gofake": `echo hello`},
	} {
		result, ran := pinned(t, "gofake 1.27.3\n", tools, "r/*.xml")

		assert.False(t, ran, name)
		assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusUnknown), step(gate.StepTests, gate.StatusUnknown)}, result.Steps, name)
		assert.False(t, result.Passed, name)
		assert.Contains(t, result.Report, "gofake", name)
	}
}

func TestPins_AFileThatCannotBeUsedVoidsTheStepAndNamesTheFile(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"no tool":                      "# nothing\n",
		"no version":                   "gofake\n",
		"name is not safe":             "go;rm 1.0\n",
		"name starts with a dash":      "-x 1.0\n",
		"name is a command substitute": "$(id) 1.0\n",
	} {
		result, ran := pinned(t, text, nil, "")

		assert.False(t, ran, name)
		assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusUnknown)}, result.Steps, name)
		assert.Contains(t, result.Report, ".tool-versions", name)
	}
}

func TestPins_AMissingPinFileVoidsTheStep(t *testing.T) {
	t.Parallel()

	g := gate.New(func(context.Context, string, string) (string, int) { return "fatal: no such path", 128 }, (&fakeTree{states: []gate.State{clean(headA), clean(headA)}}).read, nil)
	p := model.Project{Name: "demo", Gate: "make check", BuildHost: "local", BaseBranch: "main", PinFile: "absent"}

	result, err := g.Run(context.Background(), gate.Request{Project: p, Dir: t.TempDir()})

	require.NoError(t, err)
	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusUnknown)}, result.Steps)
	assert.Contains(t, result.Report, "absent")
}
