package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// registry is the adapter registry of the config tests: the names of the model, with the keys and tools that the real adapters register. It imports no adapter package.
func registry() config.Adapters {
	beads := func() *config.Entry {
		return &config.Entry{Block: &config.Block{Name: "beads", Args: []config.Arg{{Key: "prefix"}}}, Tools: []string{"bd"}}
	}
	github := func() *config.Entry { return &config.Entry{Keys: []string{"repo"}, Tools: []string{"gh"}} }

	return config.Adapters{
		Trackers: map[model.TrackerName]*config.Entry{"local": {}, "beads": beads(), "github": github()},
		Claims:   map[model.ClaimsName]*config.Entry{"local": {}, "beads": beads()},
		Forges:   map[model.ForgeName]*config.Entry{"github": github()},
		Hosts: map[model.HostProvider]*config.Entry{
			"skypilot": {Idle: true, HostBlock: "skypilot"},
			"ssh":      {HostBlock: "ssh"},
			"slurm":    {HostBlock: "ssh"},
		},
		Agents: map[model.Agent]*config.Entry{"claude": {}, "opencode": {}},
	}
}

// loadFolder loads a config folder against the registry of the tests.
func loadFolder(dir string, env map[string]string) (config.Config, config.Problems) {
	return config.LoadFolder(dir, env, registry())
}

func writeFile(t *testing.T, path string, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.Chmod(path, mode))
}
