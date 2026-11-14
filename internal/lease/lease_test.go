package lease_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/lease"
	"github.com/tobocop2/beehave/internal/model"
)

// registry declares adapters that no package implements: the lease package reads the config with whatever registry the binary gives it.
func registry() config.Adapters {
	return config.Adapters{
		Trackers: map[model.TrackerName]*config.Entry{"local": {}, "ticket": {Keys: []string{"repo"}}},
		Claims:   map[model.ClaimsName]*config.Entry{"local": {}, "ledger": {Block: &config.Block{Name: "beads", Args: []config.Arg{{Key: "prefix"}}}}},
		Forges:   map[model.ForgeName]*config.Entry{"hub": {Keys: []string{"repo"}}},
	}
}

// projectOf finds the project of a repository with the registry of the tests.
func projectOf(env map[string]string, repo string) (model.Project, error) {
	return lease.ProjectOf(env, repo, registry())
}

const project = "project(repo = \"owner/name\", tracker = \"ticket\", forge = \"hub\", claims = \"ledger\", beads = beads(prefix = \"p\"), base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\")\n"

// configFolder writes a config folder with one project file and gives the environment that names it.
func configFolder(t *testing.T, text string) map[string]string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "projects", "p.star"), []byte(text), 0o600))

	return map[string]string{"BEEHAVE_CONFIG": dir}
}

func TestOpen_NoRowsIsRefused(t *testing.T) {
	t.Parallel()

	claims, err := lease.Open(nil)

	require.Error(t, err)
	assert.Nil(t, claims)
}

func TestProjectOf_FindsTheProjectThatOwnsTheRepository(t *testing.T) {
	t.Parallel()

	found, err := projectOf(configFolder(t, project), "owner/name")

	require.NoError(t, err)
	assert.Equal(t, "p", found.Name)
	prefix, given := found.Blocks["beads"].Text("prefix")
	assert.True(t, given)
	assert.Equal(t, "p", prefix)
}

func TestProjectOf_FindsTheProjectThatOwnsTheSource(t *testing.T) {
	t.Parallel()

	env := configFolder(t, "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\")\n")

	found, err := projectOf(env, "/srv/repo")

	require.NoError(t, err)
	assert.Equal(t, "p", found.Name)
	assert.Equal(t, "/srv/repo", found.Source)

	_, err = projectOf(env, "/srv/other")
	require.Error(t, err)
}

func TestProjectOf_FindsTheProjectThatOwnsAUrlSourceByExactText(t *testing.T) {
	t.Parallel()

	env := configFolder(t, "project(source = \"git@host:o/n.git\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\")\n")

	found, err := projectOf(env, "git@host:o/n.git")

	require.NoError(t, err)
	assert.Equal(t, "git@host:o/n.git", found.Source)

	// The match is by exact text: the owner/name form of the same repository is a different string.
	_, err = projectOf(env, "o/n")
	require.Error(t, err)
}

func TestProjectOf_EmptyRepositoryMatchesNoProject(t *testing.T) {
	t.Parallel()

	_, err := projectOf(configFolder(t, project), "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no project of the beehave config has this repository")
}

func TestProjectOf_RepositoryInTwoProjectsIsRefusedAndNamesBoth(t *testing.T) {
	t.Parallel()

	env := configFolder(t, project)
	require.NoError(t, os.WriteFile(filepath.Join(env["BEEHAVE_CONFIG"], "projects", "q.star"), []byte(project), 0o600))

	_, err := projectOf(env, "owner/name")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/name is in the projects p, q")
}

func TestProjectOf_RepositoryThatNoProjectOwnsIsRefused(t *testing.T) {
	t.Parallel()

	_, err := projectOf(configFolder(t, project), "owner/other")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no project of the beehave config has this repository: owner/other")
}

func TestProjectOf_ConfigFolderThatDoesNotExistIsRefused(t *testing.T) {
	t.Parallel()

	_, err := projectOf(map[string]string{"BEEHAVE_CONFIG": filepath.Join(t.TempDir(), "missing")}, "owner/name")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestProjectOf_ConfigFolderWithoutAHomeFolderIsRefused(t *testing.T) {
	t.Setenv("HOME", "")

	_, err := projectOf(map[string]string{}, "owner/name")

	require.Error(t, err)
}
