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
			"shelf":  {Block: &config.Block{Name: "ledger", Args: []config.Arg{{Key: "id"}}}, Tools: []string{"shelf"}},
			"odd":    {Keys: []string{"no_such_key"}},
		},
		Claims: map[model.ClaimsName]*config.Entry{"local": {}, "ledger": {Block: &config.Block{Name: "ledger", Args: []config.Arg{{Key: "id"}}}, Tools: []string{"led"}}},
		Forges: map[model.ForgeName]*config.Entry{"hub": {Keys: []string{"repo"}, Tools: []string{"hubcli"}}, "vault": {Block: &config.Block{Name: "vault", Args: []config.Arg{{Key: "id"}}}}},
		Hosts: map[model.HostProvider]*config.Entry{
			"cloud": {Idle: true, Block: &config.Block{Name: "region", Args: []config.Arg{{Key: "zone", Optional: true}}}},
			"box":   {Block: machineBlock("lan")},
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
	assert.Contains(t, problems[0].Error(), `unknown forge "ticket": use one of hub, vault`)
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

func TestLoadFolder_TheKeysAndTheBlockOfAnEntryAreRequiredWhenAProjectNamesTheAdapter(t *testing.T) {
	t.Parallel()

	_, problems := loadInvented(t, call(`source = "/srv/repo", tracker = "shelf", `+inventedTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "the tracker `shelf` needs the `ledger` block. Write ledger(id = ...).")

	_, problems = loadInvented(t, call(`source = "/srv/repo", claims = "ledger", `+inventedTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "the claims `ledger` needs the `ledger` block. Write ledger(id = ...).")

	project, problems := loadInvented(t, call(`source = "/srv/repo", claims = "ledger", ledger = ledger(id = "p"), `+inventedTail))

	require.Empty(t, problems)
	assert.Equal(t, `{id="p"}`, project.Blocks["ledger"].String())

	_, problems = loadInvented(t, call(`source = "/srv/repo", forge = "hub", `+inventedTail))

	require.NotEmpty(t, problems, "the forge hub requires repo")

	_, problems = loadInvented(t, call(`source = "/srv/repo", forge = "vault", `+inventedTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "the forge `vault` needs the `vault` block. Write vault(id = ...).")

	project, problems = loadInvented(t, call(`source = "/srv/repo", forge = "vault", vault = vault(id = "v"), `+inventedTail))

	require.Empty(t, problems)
	assert.Equal(t, `{id="v"}`, project.Blocks["vault"].String())
}

func TestLoadFolder_AProjectHoldsOneBlockForEachAdapterThatDeclaresOneAndTheHostHoldsItsOwn(t *testing.T) {
	t.Parallel()

	project, problems := loadInvented(t, call(`repo = "o/n", tracker = "shelf", forge = "vault", build_host = "lane", ledger = ledger(id = "l"), vault = vault(id = "v"),
  host = host(provider = "box", disk_floor_gb = 50, lan = lan(user = "ci")), `+inventedTail))

	require.Empty(t, problems)
	assert.Equal(t, `{id="l"}`, project.Blocks["ledger"].String(), "the tracker block")
	assert.Equal(t, `{id="v"}`, project.Blocks["vault"].String(), "the forge block, held beside the tracker block")
	assert.Len(t, project.Blocks, 2)
	assert.Equal(t, `{user="ci"}`, project.Host.Block.String(), "the host block, beside both")
}

func TestLoadFolder_AKeyThatAnEntryRequiresAndNoCheckReadsIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadInvented(t, call(`source = "/srv/repo", tracker = "odd", `+inventedTail))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "needs the key `no_such_key`, and no check reads it")
}

func TestLoadFolder_TheToolsOfAProjectAreGitThenTheToolsOfItsAdaptersOnceInNameOrder(t *testing.T) {
	t.Parallel()

	project, problems := loadInvented(t, call(`repo = "o/n", tracker = "ticket", forge = "hub", claims = "ledger", ledger = ledger(id = "p"), `+inventedTail))

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

	project, problems = loadInvented(t, call(cluster+`, host = host(provider = "box", disk_floor_gb = 50, lan = lan(user = "ci", port = 22))`))

	require.Empty(t, problems)
	user, _ := project.Host.Block.Text("user")
	port, _ := project.Host.Block.Whole("port")
	assert.Equal(t, "ci", user, "the block of the chosen entry reaches the host config unread")
	assert.Equal(t, 22, port)

	_, problems = loadInvented(t, call(cluster+`, host = host(provider = "box", disk_floor_gb = 50, region = region())`))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "the `host` block holds the `region` block, and `host.provider` \"box\" does not take it")

	_, problems = loadInvented(t, call(cluster+`, host = host(provider = "nomad", disk_floor_gb = 50)`))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), `unknown host provider "nomad": use one of box, cloud`)
}
