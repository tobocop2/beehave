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
)

const oneCommandFile = "workflow \"w\" {}\n\ncommand \"a\" {\n  run = [\"true\"]\n}\n"

func TestRun_AWorkflowFileGoesToTheEngineByItsName(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	file := filepath.Join(t.TempDir(), "flow.hcl")
	writeConfig(t, filepath.Dir(file), "flow.hcl", oneCommandFile)

	code, stdout, stderr := rig.execute(t.Context(), "run", "12", "--workflow", file)

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 finished after 1 fix rounds\n", stdout)
	assert.Equal(t, []string{"12@p " + file}, rig.runner.files)
	assert.Empty(t, rig.runner.items, "the fixed item loop does not run")
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
	dir := t.TempDir()
	writeConfig(t, dir, "workflow.hcl", oneCommandFile)
	t.Chdir(dir)

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{"12@p workflow.hcl"}, rig.runner.files)
	assert.Empty(t, rig.runner.items)
}

func TestRun_AWorkflowFileThatCannotBeReadIsRefusedAndTheFixedLoopDoesNotRun(t *testing.T) {
	rig := newRig(t)
	dir := t.TempDir()
	require.NoError(t, os.Symlink("workflow.hcl", filepath.Join(dir, "workflow.hcl"))) // a link to itself: stat fails, and not with "does not exist"
	t.Chdir(dir)

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 1, code)
	assert.Contains(t, stderr, "read the workflow file workflow.hcl")
	assert.Contains(t, stderr, "too many levels of symbolic links")
	assert.Empty(t, rig.runner.items, "a file that cannot be read does not select the fixed item loop")
	assert.Empty(t, rig.runner.files)
}

func TestRun_AnOpenRunWithAPinnedFileSaysSo(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.runner.pinned = "/work/flow.hcl"

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 continues its open run with its pinned file /work/flow.hcl\nitem 12 finished after 2 fix rounds\n", stdout)
}

func TestRun_AFailureToReadTheOpenRunStopsTheCommandAndShutsTheEngineDown(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.runner.pinnedErr = errors.New("the store is locked")

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 1, code)
	assert.Contains(t, stderr, "the store is locked")
	assert.Empty(t, rig.runner.items)
	assert.Equal(t, 1, rig.runner.count())
}

func TestRun_TheItemFieldsOfTheProjectAdaptersCheckTheWorkflowFile(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	dir := t.TempDir()
	writeConfig(t, dir, "flow.hcl", "workflow \"w\" {}\ncommand \"a\" {\n  run = [item.sha]\n}\n")

	var asked model.Project

	run := func(fields func(context.Context, model.Project) ([]string, error)) (int, string) {
		var stdout, stderr bytes.Buffer

		info := cli.BuildInfo{Version: "v9", Flow: rig.factory(), ItemFields: fields}
		code := cli.Execute(t.Context(), []string{"run", "12", "--workflow", filepath.Join(dir, "flow.hcl"), "--config", rig.dir}, info, rig.env, &stdout, &stderr)

		return code, stderr.String()
	}

	code, _ := run(func(_ context.Context, project model.Project) ([]string, error) {
		asked = project

		return []string{"id", "title", "body", "sha"}, nil
	})

	require.Equal(t, 0, code)
	assert.Equal(t, "o/n", asked.Repo)

	code, stderr := run(func(context.Context, model.Project) ([]string, error) { return nil, errors.New("the forge is down") })

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "the forge is down")

	code, _ = run(nil)

	require.Equal(t, 1, code, "without the hook only id, title and body exist")
}
