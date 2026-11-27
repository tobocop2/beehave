package local_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/store/local"
)

func TestLoadFolder_TheLocalExampleProjectLoads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	text, err := os.ReadFile("project.star")
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "projects", "p.star"), text, 0o600)) //nolint:gosec // a test writes under its own temporary folder

	adapters := config.Adapters{
		Trackers: map[model.TrackerName]*config.Entry{local.Tracker: {}},
		Claims:   map[model.ClaimsName]*config.Entry{local.Claims: {}},
	}
	cfg, problems := config.LoadFolder(dir, nil, adapters)

	require.Empty(t, problems)
	require.Equal(t, model.Project{
		Name: "p", Tracker: "local", Claims: "local", Source: filepath.Join(home, "work/your-repo"), Tools: []string{"git"}, BaseBranch: "main", Sandbox: model.SandboxOn, Gate: "make check", TestReport: "reports/*.xml", BuildHost: "local", PRLabel: "made-by-beehest", MaxSpawnDepth: config.DefaultMaxSpawnDepth,
	}, cfg.Projects["p"].Project)
}
