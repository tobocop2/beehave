package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

const oneCommandFile = "workflow \"w\" {}\n\ncommand \"a\" {\n  run = [\"true\"]\n}\n"

func TestRun_AWorkflowFileGoesToTheEngineByItsName(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	file := filepath.Join(t.TempDir(), "flow.hcl")
	writeConfig(t, filepath.Dir(file), "flow.hcl", oneCommandFile)

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

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--workflow", filepath.Join(t.TempDir(), "missing.hcl"))

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
	writeConfig(t, dir, "bad.hcl", "workflow \"w\" {}\ncommand \"a\" {\n  bogus = 1\n}\n")

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--workflow", filepath.Join(dir, "bad.hcl"))

	require.Equal(t, 1, code)
	assert.Contains(t, stderr, "bad.hcl")
	assert.False(t, opened)
	assert.Empty(t, rig.runner.files)
}

func TestRun_WorkflowHclInTheCurrentFolderIsUsedWhenNoFileIsNamed(t *testing.T) {
	rig := newRig(t)
	rig.useFile = false
	dir := t.TempDir()
	writeConfig(t, dir, "workflow.hcl", oneCommandFile)
	t.Chdir(dir)

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{"12@p workflow.hcl"}, rig.runner.files)
}

func TestRun_AFolderWithoutAWorkflowFileIsRefusedAndNamesTheFlag(t *testing.T) {
	rig := newRig(t)
	rig.useFile = false
	t.Chdir(t.TempDir())

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 1, code)
	assert.Contains(t, stderr, "workflow.hcl is not in the current folder")
	assert.Contains(t, stderr, "--workflow")
	assert.Zero(t, rig.engines, "a refused run builds no engine")
	assert.Zero(t, rig.runner.count(), "a refused run launches and shuts down nothing")
	assert.Empty(t, rig.runner.files)
}

func TestRun_AnOpenRunContinuesInAFolderWithoutAWorkflowFile(t *testing.T) {
	rig := newRig(t)
	rig.useFile = false
	rig.steering.pinned = "/work/flow.hcl"
	t.Chdir(t.TempDir())

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "continues its open run with its pinned file /work/flow.hcl")
	assert.Equal(t, []string{"12@p /work/flow.hcl"}, rig.runner.files, "the run continues with the file that it pinned")
}

func TestRun_AWorkflowFileThatCannotBeReadIsRefused(t *testing.T) {
	rig := newRig(t)
	rig.useFile = false
	dir := t.TempDir()
	require.NoError(t, os.Symlink("workflow.hcl", filepath.Join(dir, "workflow.hcl"))) // a link to itself: stat fails, and not with "does not exist"
	t.Chdir(dir)

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 1, code)
	assert.Contains(t, stderr, "read the workflow file workflow.hcl")
	assert.Contains(t, stderr, "too many levels of symbolic links")
	assert.Empty(t, rig.runner.files)
}

func TestRun_AnOpenRunWithAPinnedFileSaysSo(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.pinned = "/work/flow.hcl"

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 continues its open run with its pinned file /work/flow.hcl\nitem 12 finished\n", stdout)
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

func TestRun_TheItemFieldsOfTheProjectAdaptersCheckTheWorkflowFile(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	dir := t.TempDir()
	writeConfig(t, dir, "flow.hcl", "workflow \"w\" {}\ncommand \"a\" {\n  run = [item.sha]\n}\n")

	var asked model.Project

	run := func(capabilities func(context.Context, model.Project) (workflow.Capabilities, error)) (int, string) {
		var stdout, stderr bytes.Buffer

		info := cli.BuildInfo{Version: "v9", Flow: rig.factory(), Capabilities: capabilities}
		code := cli.Execute(t.Context(), []string{"run", "12", "--workflow", filepath.Join(dir, "flow.hcl"), "--config", rig.dir}, info, rig.env, &stdout, &stderr)

		return code, stderr.String()
	}

	code, _ := run(func(_ context.Context, project model.Project) (workflow.Capabilities, error) {
		asked = project

		return workflow.Capabilities{Fields: []string{"id", "title", "body", "sha"}}, nil
	})

	require.Equal(t, 0, code)
	assert.Equal(t, "o/n", asked.Repo)

	code, stderr := run(func(context.Context, model.Project) (workflow.Capabilities, error) {
		return workflow.Capabilities{}, errors.New("the forge is down")
	})

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "the forge is down")

	code, _ = run(nil)

	require.Equal(t, 1, code, "without the hook only id, title and body exist")
}

func TestRun_AMergeStepInAProjectWithNoForgeIsRefusedBeforeAnyClaimOrRun(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	writeConfig(t, rig.dir, "projects/p.hcl", noForgeProject)

	dir := t.TempDir()
	writeConfig(t, dir, "flow.hcl", mergeFile)

	code, _, stderr := rig.execute(t.Context(), "run", "12", "--workflow", filepath.Join(dir, "flow.hcl"))

	require.Equal(t, 1, code)
	assert.Contains(t, stderr, "flow.hcl:5:1: step `merge.ship` needs a forge, and project `p` has none.")
	assert.Zero(t, rig.engines, "no engine opens, so no claim row and no run is written")
	assert.Empty(t, rig.runner.files)
	assert.False(t, rig.steering.closeOK, "no client opens either")
}
