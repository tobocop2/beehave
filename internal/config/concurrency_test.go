package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
)

func TestLoadRunners_TheConcurrencyKeyDefaultsToStaticAndTakesTheTwoModes(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "agents()\n")
	require.Empty(t, problems)
	require.Equal(t, config.ConcurrencyStatic, cfg.Agents.Concurrency)

	cfg, problems = loadRunnersText(t, "agents(concurrency = \"static\")\n")
	require.Empty(t, problems)
	require.Equal(t, config.ConcurrencyStatic, cfg.Agents.Concurrency)

	cfg, problems = loadRunnersText(t, "agents(concurrency = \"adaptive\")\n")
	require.Empty(t, problems)
	require.Equal(t, config.ConcurrencyAdaptive, cfg.Agents.Concurrency)
}

func TestLoadRunners_AnUnknownOrNonStringConcurrencyIsRefusedNamingTheKeyTheModesAndTheVariable(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"\"turbo\"", "\"\"", "\"Adaptive\""} {
		_, problems := loadRunnersText(t, "agents(concurrency = "+text+")\n")
		require.Len(t, problems, 1, text)
		require.ErrorContains(t, problems[0], "agents.concurrency is "+text+": set it to \"static\" or \"adaptive\". Set it in ", text)
		require.ErrorContains(t, problems[0], "runners.star or in BEEHAVE_AGENTS_CONCURRENCY.", text)
	}

	_, problems := loadRunnersText(t, "agents(concurrency = 1)\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "concurrency")
}

func TestLoadRunners_AdaptiveKeepsThePerLoginCeilingCheck(t *testing.T) {
	t.Parallel()

	_, problems := loadRunnersText(t, "agents(max_concurrent = 4, max_review_concurrent = 4, per_login = 5, concurrency = \"adaptive\")\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.per_login is 5")
}

func TestLoadFolder_TheConcurrencyVariableOverlaysTheFileAndIsValidated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), "agents(max_turns = 9)\n")

	cfg, problems := loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_CONCURRENCY": "adaptive"})
	require.Empty(t, problems)
	require.Equal(t, config.ConcurrencyAdaptive, cfg.Agents.Concurrency)
	require.Equal(t, 9, cfg.Agents.MaxTurns, "the variable sets only the concurrency")

	cfg, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_CONCURRENCY": ""})
	require.Empty(t, problems)
	require.Equal(t, config.ConcurrencyStatic, cfg.Agents.Concurrency, "an empty variable leaves the value")

	_, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_CONCURRENCY": "turbo"})
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.concurrency is \"turbo\": set it to \"static\" or \"adaptive\". Set it in ")
	require.ErrorContains(t, problems[0], "or in BEEHAVE_AGENTS_CONCURRENCY.")

	writeFile(t, filepath.Join(dir, "runners.star"), "agents(concurrency = \"adaptive\")\n")

	cfg, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_CONCURRENCY": "static"})
	require.Empty(t, problems)
	require.Equal(t, config.ConcurrencyStatic, cfg.Agents.Concurrency, "the variable wins over the file")
}

func TestConcurrency_KnownIsTrueForTheTwoModesOnly(t *testing.T) {
	t.Parallel()

	require.True(t, config.ConcurrencyStatic.Known())
	require.True(t, config.ConcurrencyAdaptive.Known())
	require.False(t, config.Concurrency("").Known())
	require.False(t, config.Concurrency("turbo").Known())
}
