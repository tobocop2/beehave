package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

func TestWorktrees_EnsureForAProjectWithNoRepositoryMakesAPlainFolderAndRunsNoGit(t *testing.T) {
	t.Parallel()

	trees, git, state := ensureFixture(t)
	trees.projects = map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}
	want := filepath.Join(state, "trees", "p", "bb-1")

	got, err := trees.Ensure(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.DirExists(t, want)
	assert.Empty(t, git.calls)
	assert.NoDirExists(t, filepath.Join(state, "repos", "p"))
}

func TestWorktrees_EnsureForAPlainFolderFailsWhenTheFolderCannotBeMade(t *testing.T) {
	t.Parallel()

	trees, _, _ := ensureFixture(t)
	trees.projects = map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}
	require.NoError(t, os.WriteFile(trees.root, []byte("a file"), 0o600))

	_, err := trees.Ensure(t.Context(), "p", "bb-1")

	require.ErrorContains(t, err, "make the folder of item bb-1")
}

func TestWorktrees_AResumeOfAPlainFolderFindsItAgainAndKeepsItsFiles(t *testing.T) {
	t.Parallel()

	trees, git, _ := ensureFixture(t)
	trees.projects = map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}

	first, err := trees.Ensure(t.Context(), "p", "bb-1")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(first, "notes.md"), []byte("kept"), 0o600))

	path, err := trees.Path(t.Context(), "p", "bb-1")
	require.NoError(t, err)
	assert.Equal(t, first, path)

	again, err := trees.Ensure(t.Context(), "p", "bb-1")
	require.NoError(t, err)
	assert.Equal(t, first, again)
	assert.FileExists(t, filepath.Join(again, "notes.md"))
	assert.Empty(t, git.calls)
}
