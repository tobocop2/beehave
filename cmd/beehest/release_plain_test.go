package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/flow"
	forgegithub "github.com/tobocop2/beehest/internal/forge/github"
	"github.com/tobocop2/beehest/internal/model"
)

func TestReleaseDeps_TheWorktreesOfTheClientReadTheAddressThatTheForgeNames(t *testing.T) {
	t.Parallel()

	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p", Repo: "owner/name", Forge: forgegithub.Name}}}
	deps := releaseDeps(config.Config{Projects: projects}, flow.Options{StateDir: t.TempDir()}, newAdapters(nil, nil, nil))

	trees, ok := deps.Trees.(worktrees) // the client passes the worktrees of the engine
	require.True(t, ok)

	remote, err := cloneSource(trees.projects["p"].Project, trees.forge)

	require.NoError(t, err)
	assert.Equal(t, "https://github.com/owner/name.git", remote, "the release reads the branches at the clone address of the forge")
}

func TestReleaseDeps_TheClientPlacesAForkInTheWorktreesOfTheEngine(t *testing.T) {
	t.Parallel()

	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p", Repo: "owner/name", Forge: forgegithub.Name}}}
	deps := releaseDeps(config.Config{Projects: projects}, flow.Options{StateDir: t.TempDir()}, newAdapters(nil, nil, nil))

	_, ok := deps.Place.(worktrees)

	assert.True(t, ok)
}

func TestReleaseDeps_TheClientKnowsAProjectWithNoRepositoryAndFindsItsFolderWithoutGit(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	dir := filepath.Join(state, "trees", "p", "bb-1")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}
	deps := releaseDeps(config.Config{Projects: projects}, flow.Options{StateDir: state}, newAdapters(nil, nil, nil))

	require.NotNil(t, deps.Repository)
	assert.False(t, deps.Repository("p"))

	var path string

	require.NoError(t, deps.Trees.WithTree(t.Context(), "p", "bb-1", func(dir string) { path = dir }))
	assert.Equal(t, dir, path)
}
