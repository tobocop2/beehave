package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

const oneCommandFile = "def main(item):\n    command(name = \"a\", run = [\"true\"])\n"

func TestRun_AWorkflowFileGoesToTheEngineByItsName(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	file := filepath.Join(t.TempDir(), "flow.star")
	writeConfig(t, filepath.Dir(file), "flow.star", oneCommandFile)

	code, stdout, stderr := rig.execute(t.Context(), "run", "12", "--workflow", file)

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 finished\n", stdout)
	assert.Equal(t, []string{"12@p " + file}, rig.runner.files)
}

func TestRun_AWorkflowFileThatDoesNotExistIsRefusedBeforeTheEngineOpens(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	opened := false
	rig.engineOf = func() error { opened = true; return nil }

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--workflow", filepath.Join(t.TempDir(), "missing.star"))

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "read the workflow file")
	assert.False(t, opened)
}

func TestRun_AWorkflowFileWithAProblemIsRefusedBeforeTheEngineOpens(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	opened := false
	rig.engineOf = func() error { opened = true; return nil }
	dir := t.TempDir()
	writeConfig(t, dir, "bad.star", "def main(item):\n    command(name = \"a\", run = [bogus])\n")

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--workflow", filepath.Join(dir, "bad.star"))

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "bad.star")
	assert.False(t, opened)
	assert.Empty(t, rig.runner.files)
}

func TestRun_AGateCallInAProjectWithNoRepositoryIsRefusedBeforeTheEngineOpens(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	opened := false
	rig.engineOf = func() error { opened = true; return nil }
	writeConfig(t, rig.dir, "projects/p.star", "project()\n")
	dir := t.TempDir()
	writeConfig(t, dir, "gate.star", "def main(item):\n    gate(name = \"check\")\n    gate(run = [\"true\"], name = \"links\")\n")

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--workflow", filepath.Join(dir, "gate.star"))

	require.Equal(t, 1, code, stderr)
	assert.Contains(t, stderr, "gate.star:2:9: gate needs a repository, and project p has no repository")
	assert.NotContains(t, stderr, "gate.star:3")
	assert.False(t, opened)
}

func TestRun_AProjectWithNoRepositoryRunsAWorkflowThatCallsNoGitBuiltin(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	writeConfig(t, rig.dir, "projects/p.star", "project()\n")

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 finished\n", stdout)
}

func TestRun_WorkflowStarInTheCurrentFolderIsUsedWhenNoFileIsNamed(t *testing.T) {
	rig := newRig(t)
	rig.useFile = false
	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", oneCommandFile)
	t.Chdir(dir)

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{"12@p workflow.star"}, rig.runner.files)
}

func TestRun_AFolderWithoutAWorkflowFileIsRefusedAndNamesTheFlag(t *testing.T) {
	rig := newRig(t)
	rig.useFile = false
	t.Chdir(t.TempDir())

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 1, code)
	assert.Contains(t, stderr, "workflow.star is not in the current folder")
	assert.Contains(t, stderr, "--workflow")
	assert.Zero(t, rig.engines, "a refused run builds no engine")
	assert.Zero(t, rig.runner.count(), "a refused run launches and shuts down nothing")
	assert.Empty(t, rig.runner.files)
}

func TestRun_AnOpenRunContinuesInAFolderWithoutAWorkflowFile(t *testing.T) {
	rig := newRig(t)
	rig.useFile = false
	rig.steering.pinned = "/work/flow.star"
	t.Chdir(t.TempDir())

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "continues its open run with its pinned file /work/flow.star")
	assert.Equal(t, []string{"12@p /work/flow.star"}, rig.runner.files, "the run continues with the file that it pinned")
}

func TestRun_AWorkflowFileThatCannotBeReadIsRefused(t *testing.T) {
	rig := newRig(t)
	rig.useFile = false
	dir := t.TempDir()
	require.NoError(t, os.Symlink("workflow.star", filepath.Join(dir, "workflow.star"))) // a link to itself: stat fails, and not with "does not exist"
	t.Chdir(dir)

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 1, code)
	assert.Contains(t, stderr, "read the workflow file workflow.star")
	assert.Contains(t, stderr, "too many levels of symbolic links")
	assert.Empty(t, rig.runner.files)
}

func TestRun_AnOpenRunWithAPinnedFileSaysSo(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.pinned = "/work/flow.star"

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 continues its open run with its pinned file /work/flow.star\nitem 12 finished\n", stdout)
}

func TestRun_AFailureToReadTheOpenRunStopsTheCommandBeforeAnyEngineOpens(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.pinnedErr = errors.New("the store is locked")

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 1, code)
	assert.Contains(t, stderr, "the store is locked")
	assert.Empty(t, rig.runner.items)
	assert.Zero(t, rig.engines)
	assert.True(t, rig.steering.closeOK, "the client that read the open run is closed")
}

func TestRun_ARunThatCannotOpenTheClientBuildsNoEngine(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.clientOf = func() error { return errFake }

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 1, code)
	assert.Contains(t, stderr, errFake.Error())
	assert.Zero(t, rig.engines)
}

func TestRun_ASandboxNoneStepInAProjectWithTheSandboxOnIsRefusedBeforeTheEngineOpens(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	opened := false
	rig.engineOf = func() error { opened = true; return nil }
	writeConfig(t, rig.dir, "projects/p.star", "project(sandbox = \"on\")\n")
	dir := t.TempDir()
	writeConfig(t, dir, "none.star", "def main(item):\n    agent(prompt = \"p\", model = \"m\", sandbox = None)\n")

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--workflow", filepath.Join(dir, "none.star"))

	require.Equal(t, 1, code, stderr)
	assert.Contains(t, stderr, "none.star:2:48: sandbox is None, and project p has the sandbox on")
	assert.False(t, opened)
	assert.Empty(t, rig.runner.files)
}

func TestRun_AProjectWithTheSandboxOffPrintsTheOffLineAtTheStartAndOtherProjectsPrintNone(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		project string
		line    bool
	}{"off": {"project(sandbox = \"off\")\n", true}, "on": {"project(sandbox = \"on\")\n", false}, "unset": {"project()\n", false}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)
			writeConfig(t, rig.dir, "projects/p.star", test.project)

			code, _, stderr := rig.execute(t.Context(), "run", "12")

			require.Equal(t, 0, code, stderr)
			assert.Equal(t, test.line, strings.Contains(stderr, `item 12: project p has sandbox = "off"`), stderr)
		})
	}
}

func TestRun_AnOpenRunPrintsTheOffLineFromItsStoredModeNotFromTheProjectFile(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		project string
		stored  model.SandboxMode
		line    bool
	}{
		"stored on, project off":    {"project(sandbox = \"off\")\n", model.SandboxOn, false},
		"stored off, project on":    {"project(sandbox = \"on\")\n", model.SandboxOff, true},
		"stored off, project unset": {"project()\n", model.SandboxOff, true},
		"no stored mode, off":       {"project(sandbox = \"off\")\n", "", true},
		"no stored mode, on":        {"project(sandbox = \"on\")\n", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)
			writeConfig(t, rig.dir, "projects/p.star", test.project)
			rig.steering.openRun, rig.steering.openMode = true, test.stored

			code, _, stderr := rig.execute(t.Context(), "run", "12")

			require.Equal(t, 0, code, stderr)
			assert.Equal(t, test.line, strings.Contains(stderr, model.SandboxOffLine), stderr)
		})
	}
}

func TestRun_AFailureToReadTheStoredModeOfTheOpenRunStopsTheCommandBeforeAnyEngineOpens(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.openErr = errors.New("the store is locked")

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "the store is locked")
	assert.Empty(t, rig.runner.files)
}
