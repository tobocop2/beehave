package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
)

func TestInit_AQuoteAndANewlineInTheGateComeBackAsTheSameGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	confDir := t.TempDir()
	writeConfig(t, confDir, "runners.star", validRunners)
	initRepo(t, "main")

	const gate = "make \"check\"\nnow \\ \"), gate = \"x"

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir, "--gate", gate, "--test-report", "a\"b")
	require.Equal(t, 0, code, stderr)

	cfg, problems := config.LoadFolder(confDir, map[string]string{}, registry())

	require.Empty(t, problems)
	assert.Equal(t, gate, cfg.Projects["work"].Gate)
	assert.Equal(t, "a\"b", cfg.Projects["work"].TestReport)
}
