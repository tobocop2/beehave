package config_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/config"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store/beads"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
)

// registry is the adapter registry that the example config names: GitHub issues and forge, beads claims and the Claude agent.
func registry() config.Adapters {
	beadsEntry := func() *config.Entry {
		block := beads.Block()

		return &config.Entry{Block: &block, Tools: []string{"bd"}}
	}
	githubEntry := func() *config.Entry { return &config.Entry{Keys: []string{"repo"}, Tools: []string{"gh"}} }

	return config.Adapters{
		Trackers: map[model.TrackerName]*config.Entry{beads.Tracker: beadsEntry(), storegithub.Tracker: githubEntry()},
		Claims:   map[model.ClaimsName]*config.Entry{beads.Claims: beadsEntry()},
		Forges:   map[model.ForgeName]*config.Entry{forgegithub.Name: githubEntry()},
		Agents:   map[model.Agent]*config.Entry{claude.Name: {Windows: claude.New(nil).Profile().Windows}},
	}
}

func TestLoadFolder_TheExampleConfigLoads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, problems := config.LoadFolder(".", nil, registry())

	require.Empty(t, problems)
	require.Equal(t, []model.Runner{
		{Name: "claude-a", Agent: "claude", ConfigDir: filepath.Join(home, ".claude-profiles/a")},
		{Name: "claude-b", Agent: "claude", ConfigDir: filepath.Join(home, ".claude-profiles/b")},
	}, cfg.Runners)
	require.Equal(t, config.Pool{"five_hour": 90, "seven_day": 95}, cfg.Pools["claude"])
	require.Equal(t, 2, cfg.Agents.MaxConcurrent)
	require.Len(t, cfg.Projects, 1)
	require.Equal(t, model.Project{Name: "example", Repo: "your-org/your-repo", Tracker: "github", Claims: "beads", Forge: "github", Blocks: map[string]*model.Block{"beads": model.NewBlock(filepath.Join(".", "projects", "example.star")+":12:18", []model.BlockEntry{model.TextEntry("prefix", "proj")})}, Tools: []string{"git", "bd", "gh"}, BaseBranch: "main", Gate: "make check", TestReport: "reports/*.xml", BuildHost: "local", PRLabel: "made-by-beehave", MaxSpawnDepth: config.DefaultMaxSpawnDepth, Sandbox: model.SandboxOn},
		cfg.Projects["example"].Project)
}

func TestLoadFolder_TheExampleHoldsNoPlaceholderOrOwnerPath(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"runners.star", "projects/example.star"} {
		text, err := fs.ReadFile(os.DirFS("."), name)
		require.NoError(t, err)
		require.NotContains(t, string(text), "decided in step")
		require.NotContains(t, string(text), "/Users/")
	}
}
