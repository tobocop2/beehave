package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
)

func TestReleaseDeps_TheClientKnowsAProjectWithNoRepositoryAndFindsItsFolderWithoutGit(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	dir := filepath.Join(state, "trees", "p", "bb-1")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}
	deps := releaseDeps(itemLife{}, projects, flow.Options{StateDir: state})

	require.NotNil(t, deps.Repository)
	assert.False(t, deps.Repository("p"))

	path, err := deps.Trees.Path(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	assert.Equal(t, dir, path)
}
