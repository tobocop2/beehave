package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// invented declares adapters that no package implements: the config reads whatever registry the build gives it.
func invented() config.Adapters {
	return config.Adapters{
		Trackers: map[model.TrackerName]*config.Entry{
			"local":  {},
			"ticket": {Keys: []string{"repo"}, Tools: []string{"tk", "git"}},
			"shelf":  {Keys: []string{"beads_prefix"}, Tools: []string{"shelf"}},
			"odd":    {Keys: []string{"no_such_key"}},
		},
		Claims: map[model.ClaimsName]*config.Entry{"local": {}, "ledger": {Keys: []string{"beads_prefix"}, Tools: []string{"led"}}},
		Forges: map[model.ForgeName]*config.Entry{"hub": {Keys: []string{"repo"}, Tools: []string{"hubcli"}}},
		Hosts: map[model.HostProvider]*config.Entry{
			"cloud": {Idle: true, HostBlock: "skypilot"},
			"box":   {HostBlock: "ssh"},
		},
		Agents: map[model.Agent]*config.Entry{"robot": {}, "droid": {}},
	}
}

func loadInvented(t *testing.T, text string) (config.Project, config.Problems) {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "projects", "p.star"), text)
	cfg, problems := config.LoadFolder(dir, nil, invented())

	return cfg.Projects["p"], problems
}

const inventedTail = `base_branch = "main", gate = "make check", test_report = "r.xml"`

func TestLoadFolder_ANameOfTheRegistryLoadsAndAnUnregisteredNameListsTheRegisteredOnes(t *testing.T) {
	t.Parallel()

	project, problems := loadInvented(t, call(`repo = "o/n", tracker = "ticket", forge = "hub", `+inventedTail))

	require.Empty(t, problems)
	assert.Equal(t, model.TrackerName("ticket"), project.Tracker)
	assert.Equal(t, model.ForgeName("hub"), project.Forge)

	_, problems = loadInvented(t, call(`repo = "o/n", tracker = "github", `+inventedTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), `unknown tracker "github": use one of local, odd, shelf, ticket`)

	_, problems = loadInvented(t, call(`repo = "o/n", forge = "ticket", `+inventedTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), `unknown forge "ticket": use one of hub`)
}

func TestLoadFolder_AnAgentOfTheRegistryIsARunnerAgentAndTheRefusalListsTheRegisteredAgents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), "runner(name = \"a\", agent = \"gpt\")\n")

	_, problems := config.LoadFolder(dir, nil, invented())

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), `unknown agent "gpt": use one of droid, robot`)

	writeFile(t, filepath.Join(dir, "runners.star"), "runner(name = \"a\", agent = \"robot\")\n")

	cfg, problems := config.LoadFolder(dir, nil, invented())

	require.Empty(t, problems)
	require.Len(t, cfg.Runners, 1)
	assert.Equal(t, model.Agent("robot"), cfg.Runners[0].Agent)
}

func TestLoadFolder_TheKeysOfAnEntryAreRequiredWhenAProjectNamesTheAdapter(t *testing.T) {
	t.Parallel()

	_, problems := loadInvented(t, call(`source = "/srv/repo", tracker = "shelf", `+inventedTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "`beads_prefix` must be a non-empty string")

	_, problems = loadInvented(t, call(`source = "/srv/repo", claims = "ledger", `+inventedTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "`beads_prefix` must be a non-empty string")

	project, problems := loadInvented(t, call(`source = "/srv/repo", claims = "ledger", beads_prefix = "p", `+inventedTail))

	require.Empty(t, problems)
	assert.Equal(t, "p", project.BeadsPrefix)

	_, problems = loadInvented(t, call(`source = "/srv/repo", forge = "hub", `+inventedTail))

	require.NotEmpty(t, problems, "the forge hub requires repo")
}

func TestLoadFolder_AKeyThatAnEntryRequiresAndNoCheckReadsIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadInvented(t, call(`source = "/srv/repo", tracker = "odd", `+inventedTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "needs the key `no_such_key`, and no check reads it")
}

func TestLoadFolder_TheToolsOfAProjectAreGitThenTheToolsOfItsAdaptersOnceInNameOrder(t *testing.T) {
	t.Parallel()

	project, problems := loadInvented(t, call(`repo = "o/n", tracker = "ticket", forge = "hub", claims = "ledger", beads_prefix = "p", `+inventedTail))

	require.Empty(t, problems)
	assert.Equal(t, []string{model.ToolGit, "hubcli", "led", "tk"}, project.Tools)

	project, problems = loadInvented(t, call(`source = "/srv/repo", `+inventedTail))

	require.Empty(t, problems)
	assert.Equal(t, []string{model.ToolGit}, project.Tools, "the local adapters need no program besides git")
}

func TestLoadFolder_OnlyAHostEntryWithIdleGetsIdleMinutesAndTheHostBlockComesFromTheEntry(t *testing.T) {
	t.Parallel()

	const cluster = `repo = "o/n", tracker = "ticket", build_host = "lane", ` + inventedTail

	project, problems := loadInvented(t, call(cluster+`, host = host(provider = "cloud", disk_floor_gb = 50)`))

	require.Empty(t, problems)
	assert.Equal(t, model.HostProvider("cloud"), project.Host.Provider)
	assert.Positive(t, project.Host.IdleMinutes, "a host entry with Idle gets the default idle_minutes")

	project, problems = loadInvented(t, call(cluster+`, host = host(provider = "box", disk_floor_gb = 50)`))

	require.Empty(t, problems)
	assert.Zero(t, project.Host.IdleMinutes)

	_, problems = loadInvented(t, call(cluster+`, host = host(provider = "box", disk_floor_gb = 50, idle_minutes = 30)`))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "`host.idle_minutes` is not allowed for provider \"box\"")

	_, problems = loadInvented(t, call(cluster+`, host = host(provider = "box", disk_floor_gb = 50, skypilot = skypilot())`))

	require.NotEmpty(t, problems, "the box entry takes the ssh block and no other")

	_, problems = loadInvented(t, call(cluster+`, host = host(provider = "nomad", disk_floor_gb = 50)`))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), `unknown host provider "nomad": use one of box, cloud`)
}
