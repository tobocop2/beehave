package claude_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
)

// attributionOff is the value of the attribution setting that omits the commit trailer, the pull request text and the session link, in the form that every
// Claude Code version reads.
const attributionOff = `{"commit":"","pr":"","sessionUrl":false}`

// attributionSettings is the whole --settings value of a launch that has no hook, no output file and no skill.
const attributionSettings = `{"attribution":` + attributionOff + `}`

func TestAttribution_EveryLaunchTurnsOffTheCommitTrailerInItsSettings(t *testing.T) {
	t.Parallel()

	adapter := claude.New(nil)
	launches := map[string]agent.Launch{
		"guard with a hook":            adapter.Guard("/bin/beehave check command", "/run", nil, nil),
		"guard with no hook and files": adapter.Guard("", "/run", nil, nil),
		"guard with an output file":    adapter.Guard("", "/run", []string{outputFile}, nil),
		"sandbox":                      adapter.Sandbox(plan(), "", "/run", nil, nil),
		"sandbox with a hook":          adapter.Sandbox(plan(), "g", "/run", []string{outputFile}, nil),
	}

	for name, launch := range launches {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, settings := settingsOf(t, launch.Args)

			attribution, err := json.Marshal(settings["attribution"])
			require.NoError(t, err)
			assert.JSONEq(t, attributionOff, string(attribution), "the CLI adds its Co-Authored-By trailer unless the settings turn it off")
		})
	}
}

func TestLaunch_SaysWhetherItHoldsTheHook(t *testing.T) {
	t.Parallel()

	adapter := claude.New(nil)

	assert.True(t, adapter.Guard("/bin/beehave check command", "/run", nil, nil).Hooked)
	assert.False(t, adapter.Guard("", "/run", []string{outputFile}, nil).Hooked, "settings with no hook")
	assert.True(t, adapter.Sandbox(plan(), "g", "/run", nil, nil).Hooked)
	assert.False(t, adapter.Sandbox(plan(), "", "/run", nil, nil).Hooked)
}
