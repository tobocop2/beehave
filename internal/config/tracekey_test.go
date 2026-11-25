package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadTraceKey(t *testing.T, runners string, env map[string]string) (bool, string) {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), runners)

	cfg, problems := loadFolder(dir, env)

	return cfg.Agents.TraceParent, problems.Error()
}

func TestConfig_TheTraceParentKeyIsOffByDefault(t *testing.T) {
	t.Parallel()

	on, problems := loadTraceKey(t, "agents(max_turns = 3)\n", nil)

	assert.False(t, on)
	assert.Empty(t, problems)
}

func TestConfig_TheTraceParentKeyOfTheFileTurnsItOn(t *testing.T) {
	t.Parallel()

	on, problems := loadTraceKey(t, "agents(trace_parent = True)\n", nil)

	assert.True(t, on)
	assert.Empty(t, problems)
}

func TestConfig_TheTraceParentVariableOverlaysTheFileInBothDirections(t *testing.T) {
	t.Parallel()

	off, problems := loadTraceKey(t, "agents(trace_parent = True)\n", map[string]string{"BEEHAVE_AGENTS_TRACE_PARENT": "false"})
	require.Empty(t, problems)
	assert.False(t, off)

	on, problems := loadTraceKey(t, "agents(trace_parent = False)\n", map[string]string{"BEEHAVE_AGENTS_TRACE_PARENT": "true"})
	require.Empty(t, problems)
	assert.True(t, on)

	kept, problems := loadTraceKey(t, "agents(trace_parent = True)\n", map[string]string{"BEEHAVE_AGENTS_TRACE_PARENT": ""})
	require.Empty(t, problems)
	assert.True(t, kept, "an empty variable leaves the value of the file")
}

func TestConfig_ATraceParentVariableThatIsNoBooleanIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	_, problems := loadTraceKey(t, "agents(max_turns = 3)\n", map[string]string{"BEEHAVE_AGENTS_TRACE_PARENT": "maybe"})

	assert.Contains(t, problems, "BEEHAVE_AGENTS_TRACE_PARENT=maybe")
}

func TestConfig_ATraceParentVariableOtherThanTrueOrFalseIsRefused(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"1", "t", "T", "0", "f", "F", "TRUE", "True", "yes"} {
		_, problems := loadTraceKey(t, "agents(max_turns = 3)\n", map[string]string{"BEEHAVE_AGENTS_TRACE_PARENT": value})

		assert.Contains(t, problems, "BEEHAVE_AGENTS_TRACE_PARENT="+value, value)
	}
}

func TestConfig_ATraceParentOfTheFileThatIsNoBooleanIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadTraceKey(t, "agents(trace_parent = \"yes\")\n", nil)

	assert.Contains(t, problems, "trace_parent")
	assert.NotContains(t, problems, "unexpected keyword")
}
