package lease_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/lease"
)

const project = "repo = \"owner/name\"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\nbase_branch = \"main\"\ngate = \"make check\"\n"

// configFolder writes a config folder with one project file and gives the environment that names it.
func configFolder(t *testing.T, text string) map[string]string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "projects", "p.hcl"), []byte(text), 0o600))

	return map[string]string{"OBEE_CONFIG": dir}
}

func TestOpen_NoRowsIsRefused(t *testing.T) {
	t.Parallel()

	claims, err := lease.Open(nil)

	require.Error(t, err)
	assert.Nil(t, claims)
}

func TestProjectOf_FindsTheProjectThatOwnsTheRepository(t *testing.T) {
	t.Parallel()

	found, err := lease.ProjectOf(configFolder(t, project), "owner/name")

	require.NoError(t, err)
	assert.Equal(t, "p", found.Name)
	assert.Equal(t, "p", found.BeadsPrefix)
}

func TestProjectOf_RepositoryInTwoProjectsIsRefusedAndNamesBoth(t *testing.T) {
	t.Parallel()

	env := configFolder(t, project)
	require.NoError(t, os.WriteFile(filepath.Join(env["OBEE_CONFIG"], "projects", "q.hcl"), []byte(project), 0o600))

	_, err := lease.ProjectOf(env, "owner/name")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/name is in the projects p, q")
}

func TestProjectOf_RepositoryThatNoProjectOwnsIsRefused(t *testing.T) {
	t.Parallel()

	_, err := lease.ProjectOf(configFolder(t, project), "owner/other")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no project of the obee config has this repository: owner/other")
}

func TestProjectOf_ConfigFolderThatDoesNotExistIsRefused(t *testing.T) {
	t.Parallel()

	_, err := lease.ProjectOf(map[string]string{"OBEE_CONFIG": filepath.Join(t.TempDir(), "missing")}, "owner/name")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestProjectOf_ConfigFolderWithoutAHomeFolderIsRefused(t *testing.T) {
	t.Setenv("HOME", "")

	_, err := lease.ProjectOf(map[string]string{}, "owner/name")

	require.Error(t, err)
}
