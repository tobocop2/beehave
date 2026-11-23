package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
)

// fillCall is one call of the module filler of a rig.
type fillCall struct{ project, dir string }

// recordFills makes the module filler of the rig record each call and return cache with note.
func (r *sandboxRig) recordFills(cache, note string) *[]fillCall {
	var calls []fillCall

	r.f.agents.modules = func(_ context.Context, project, dir string) (string, string) {
		calls = append(calls, fillCall{project, dir})

		return cache, note
	}

	return &calls
}

func TestSandboxPlan_FillsTheModuleCacheOfTheProject(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	calls := r.recordFills("/state/gomodcache/p", "")
	req := request()
	req.Sandbox = &model.Sandbox{}

	result, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, []fillCall{{"p", r.work}}, *calls, "the fill runs once, in the worktree of the item")
	require.NotNil(t, r.f.spec.Sandbox)
	assert.Equal(t, "/state/gomodcache/p", r.f.spec.Sandbox.ModuleCache)
	assert.Empty(t, result.Note)
}

func TestSandboxPlan_AFillThatFailedIsANoteOfAStepThatStarts(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	r.recordFills("/state/gomodcache/p", "the Go module download failed")
	req := request()
	req.Sandbox = &model.Sandbox{}

	result, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeExited, result.Outcome)
	assert.Contains(t, result.Note, "the Go module download failed")
}

func TestSandboxPlan_AStepWithNoSandboxRunsNoDownload(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	calls := r.recordFills("/state/gomodcache/p", "")

	_, err := r.f.agents.Run(t.Context(), request())

	require.NoError(t, err)
	assert.Empty(t, *calls)
	assert.Nil(t, r.f.spec.Sandbox)
}

func TestSandboxPlan_AStepThatDoesNotStartRunsNoDownload(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	calls := r.recordFills("/state/gomodcache/p", "")
	req := request()
	req.Sandbox = &model.Sandbox{}
	req.Skills = []string{"missing"}

	result, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Empty(t, *calls)
}

func TestSandboxPlan_TwoProjectsGetTwoCaches(t *testing.T) {
	t.Parallel()

	state, bin := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\n[ \"$2\" = edit ] && echo '{}'\nexit 0\n"), 0o700)) //nolint:gosec // the fake go command must be executable

	fill := newAgents(config.Config{}, flow.Options{StateDir: state, Env: map[string]string{"PATH": bin}}, treeAt(""), newAdapters(nil, nil, nil), nil).modules

	caches := map[string]string{}

	for _, project := range []string{"alpha", "beta"} {
		work := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/"+project+"\n"), 0o600))

		cache, note := fill(t.Context(), project, work)
		require.Empty(t, note)

		caches[project] = cache
	}

	assert.Equal(t, filepath.Join(state, "gomodcache", "alpha"), caches["alpha"])
	assert.Equal(t, filepath.Join(state, "gomodcache", "beta"), caches["beta"])
	assert.DirExists(t, caches["alpha"])
	assert.DirExists(t, caches["beta"])
}
