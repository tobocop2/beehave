package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

const claimProject = "project(repo = \"owner/name\", tracker = \"github\", forge = \"github\", claims = \"beads\", beads_prefix = \"p\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\")\n"

// configEnv writes a config folder with one project file and gives the environment that names it.
func configEnv(t *testing.T) map[string]string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), treeFolderMode))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "projects", "p.star"), []byte(claimProject), 0o600))

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

	claims, err := claimsOf(proctest.Always(model.CommandResult{}), nil, model.Project{Claims: model.ClaimsBeads})

	require.Error(t, err)
	assert.Nil(t, claims)
	assert.Contains(t, err.Error(), "beads id prefix")
}

func stateAt(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

func TestClaimsOf_LocalClaimsAreTheLocalDbOfTheStateFolder(t *testing.T) {
	t.Parallel()

	state := t.TempDir()

	claims, err := claimsOf(proctest.Always(model.CommandResult{}), stateAt(state), model.Project{Name: "p", Claims: model.ClaimsLocal})

	require.NoError(t, err)
	require.NoError(t, claims.Take(t.Context(), "ownerone", "fix-login"))
	assert.FileExists(t, filepath.Join(state, "local.db"))
}

func TestClaimsOf_LocalClaimsInAStateFolderThatCannotBeResolvedAreRefused(t *testing.T) {
	t.Parallel()

	failing := func() (string, error) { return "", errors.New("no home folder") }

	claims, err := claimsOf(proctest.Always(model.CommandResult{}), failing, model.Project{Name: "p", Claims: model.ClaimsLocal})

	require.ErrorContains(t, err, "no home folder")
	assert.Nil(t, claims)
}

func TestClaimsOf_LocalClaimsInAStateFolderThatCannotBeOpenedAreRefused(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	claims, err := claimsOf(proctest.Always(model.CommandResult{}), stateAt(filepath.Join(file, "state")), model.Project{Name: "p", Claims: model.ClaimsLocal})

	require.Error(t, err)
	assert.Nil(t, claims)
}

func TestOpenClaims_OpensTheLocalClaimsOfALocalProjectInTheStateFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), treeFolderMode))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "projects", "p.star"), []byte("project(source = \"/srv/repo\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\")\n"), 0o600))
	state := filepath.Join(t.TempDir(), "state")

	claims, err := openClaims(map[string]string{"OBEE_CONFIG": dir, "OBEE_STATE_DIR": state}, proctest.Always(model.CommandResult{}))("/srv/repo")

	require.NoError(t, err)
	require.NoError(t, claims.Take(t.Context(), "ownerone", "fix-login"))
	assert.FileExists(t, filepath.Join(state, "local.db"))
}

func TestClaimsOf_UnknownClaimStoreIsRefused(t *testing.T) {
	t.Parallel()

	claims, err := claimsOf(proctest.Always(model.CommandResult{}), nil, model.Project{Name: "p", Claims: "nope"})

	require.Error(t, err)
	assert.Nil(t, claims)
	assert.Contains(t, err.Error(), `no claim store named "nope"`)
}
