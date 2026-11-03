package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
)

func TestProjectGate_TheGateCommandOfAPlainFolderRunsWithTheFolderReadOnly(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	dir := t.TempDir()
	gate := projectGate{projects: sandboxedProject(model.SandboxOn, config.LocalHost), programs: programs{sandbox: sandboxFor(t, fake, nil)}}

	_, code := gate.shellFor("p")(t.Context(), dir, "make check")

	require.Zero(t, code)
	assert.Equal(t, []string{"sh", "-c", "make check"}, fake.argv)
	assert.Contains(t, fake.plan.Read, dir)
	assert.NotContains(t, fake.plan.Write, dir, "the project gate of a folder with no .git has no copy, so the folder is read-only")
	assert.Contains(t, fake.plan.Write, filepath.Join(dir, "cache"), "a report folder is written through gate_allow_write")
}

func TestGateSandbox_AGateInAPlainFolderMayReadItAndNeverWritesIt(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	dir := t.TempDir()

	_, code := sandboxFor(t, fake, nil).run(t.Context(), "p", dir, []string{"true"}, nil, 0)

	require.Zero(t, code)
	assert.Contains(t, fake.plan.Read, dir)
	assert.NotContains(t, fake.plan.Write, dir, "a plain folder has no disposable copy, so the plan cannot grant it for writing")
	assert.Contains(t, fake.plan.Write, filepath.Join(dir, "cache"), "gate_allow_write still opens its own path")
}

func TestGateSandbox_AGateInAPlainFolderRefusesAWritePathThatHoldsTheFolder(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	box := sandboxFor(t, fake, nil)
	project := box.projects["p"]
	project.GateAllowWrite = []string{"."}
	box.projects["p"] = project

	out, code := box.run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "overlaps the protected path")
	assert.Nil(t, fake.argv, "the gate ran no code")
}

func TestPrograms_AProgramGateOfAnItemWithNoFolderWorksInAFolderOfItsOwn(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	runs := programs{trees: treeAt(""), sandbox: sandboxFor(t, fake, nil)}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"true"}, Gate: true})
	require.NoError(t, err)
	assert.Contains(t, fake.plan.Write, fake.dir, "the empty folder is made for the gate, so the gate may write it")
}

func TestPrograms_AProgramGateOfAGitProjectWritesTheCopyAndNotTheFolder(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	dir := repoWithCommit(t)
	runs := programs{trees: treeAt(dir), sandbox: sandboxFor(t, fake, nil)}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"true"}, Gate: true})
	require.NoError(t, err)
	assert.NotEqual(t, dir, fake.dir)
	assert.Contains(t, fake.plan.Write, fake.dir)
	assert.NotContains(t, fake.plan.Write, dir)
}
