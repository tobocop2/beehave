package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// twoAgentRegistry has two agents that share the window `daily` with one default, and an agent with no window.
func twoAgentRegistry(sharedDefault float64) config.Adapters {
	reg := registry()
	reg.Agents = map[model.Agent]*config.Entry{
		"alpha": {Windows: []model.WindowSpec{{Name: "daily", DefaultMax: 80}}},
		"beta":  {Windows: []model.WindowSpec{{Name: "daily", DefaultMax: sharedDefault}, {Name: "weekly", DefaultMax: 70}}},
		"none":  {},
	}

	return reg
}

func loadWithRegistry(t *testing.T, reg config.Adapters, text string, env map[string]string) (config.Config, config.Problems) {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), text)

	return config.LoadFolder(dir, env, reg)
}

func TestPoolWindows_TwoAgentsThatShareAWindowNameShareItsCap(t *testing.T) {
	t.Parallel()

	cfg, problems := loadWithRegistry(t, twoAgentRegistry(80), "pool(daily = 10)\n", nil)
	require.Empty(t, problems)
	require.Equal(t, config.Pool{"daily": 10}, cfg.Pools["alpha"])
	require.Equal(t, config.Pool{"daily": 10, "weekly": 70}, cfg.Pools["beta"], "the other window of beta keeps its default")
	require.Empty(t, cfg.Pools["none"], "an agent with no window has no caps")
}

func TestPoolWindows_TwoDefaultsForOneWindowNameRefuseTheRegistry(t *testing.T) {
	t.Parallel()

	_, problems := loadWithRegistry(t, twoAgentRegistry(60), "pool()\n", nil)
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "the agents `alpha` and `beta` declare the usage window `daily` with the default caps 80 and 60")
}

func TestPoolWindows_TwoDefaultsForOneWindowNameRefuseTheRegistryWithNoRunnersFile(t *testing.T) {
	t.Parallel()

	_, problems := config.LoadFolder(t.TempDir(), nil, twoAgentRegistry(60))
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "declare the usage window `daily`")
}

func TestPoolWindows_TheEnvironmentCapOfASharedWindowReachesEveryAgentOfIt(t *testing.T) {
	t.Parallel()

	cfg, problems := loadWithRegistry(t, twoAgentRegistry(80), "pool(daily = 10)\n", map[string]string{"BEEHAVE_POOL_DAILY_MAX": "33"})
	require.Empty(t, problems)
	require.InDelta(t, 33.0, cfg.Pools["alpha"]["daily"], 0)
	require.InDelta(t, 33.0, cfg.Pools["beta"]["daily"], 0)
	require.InDelta(t, 70.0, cfg.Pools["beta"]["weekly"], 0)
}

func TestPoolWindows_TwoBadEnvironmentCapsAreReportedInTheOrderOfTheWindowNames(t *testing.T) {
	t.Parallel()

	env := map[string]string{"BEEHAVE_POOL_WEEKLY_MAX": "lots", "BEEHAVE_POOL_DAILY_MAX": "many"}
	_, problems := loadWithRegistry(t, twoAgentRegistry(80), "pool()\n", env)
	require.Len(t, problems, 1)
	require.Regexp(t, `BEEHAVE_POOL_DAILY_MAX=many is not a number[\s\S]*BEEHAVE_POOL_WEEKLY_MAX=lots is not a number`, problems[0].Error())
}

func TestPoolWindows_APoolKeyWhenNoAgentDeclaresAWindowSaysSo(t *testing.T) {
	t.Parallel()

	reg := registry()
	reg.Agents = map[model.Agent]*config.Entry{"opencode": {}}

	_, problems := loadWithRegistry(t, reg, "pool(five_hour = 5)\n", nil)
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "`five_hour` is not a usage window: no agent declares one.")
}

func TestPoolWindows_AnEnvironmentCapThatIsNotANumberIsRefusedAndNamesTheVariable(t *testing.T) {
	t.Parallel()

	_, problems := loadWithRegistry(t, twoAgentRegistry(80), "pool()\n", map[string]string{"BEEHAVE_POOL_WEEKLY_MAX": "lots"})
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "BEEHAVE_POOL_WEEKLY_MAX=lots is not a number")
}
