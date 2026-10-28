package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

const claimProject = "repo = \"owner/name\"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\nbase_branch = \"main\"\ngate = \"make check\"\n"

// configEnv writes a config folder with one project file and gives the environment that names it.
func configEnv(t *testing.T) map[string]string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), treeFolderMode))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "projects", "p.hcl"), []byte(claimProject), 0o600))

	return map[string]string{"OBEE_CONFIG": dir}
}

func TestOpenClaims_OpensTheClaimStoreOfTheProjectThatOwnsTheRepository(t *testing.T) {
	t.Parallel()

	claims, err := openClaims(configEnv(t), proctest.Always(model.CommandResult{}))("owner/name")

	require.NoError(t, err)
	assert.NotNil(t, claims)
}

func TestOpenClaims_RepositoryThatNoProjectOwnsIsRefused(t *testing.T) {
	t.Parallel()

	claims, err := openClaims(configEnv(t), proctest.Always(model.CommandResult{}))("owner/other")

	require.Error(t, err)
	assert.Nil(t, claims)
	assert.Contains(t, err.Error(), "owner/other")
}

func TestClaimsOf_ProjectWithNoBeadsPrefixIsRefused(t *testing.T) {
	t.Parallel()

	claims, err := claimsOf(proctest.Always(model.CommandResult{}), model.Project{})

	require.Error(t, err)
	assert.Nil(t, claims)
	assert.Contains(t, err.Error(), "beads id prefix")
}
