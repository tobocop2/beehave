package opencode_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/opencode"
)

const pluginPath = "config/plugins/beehave-guard.js"

// closedEnv is what a run with a guard sets so that no file of the owner or of the checkout adds a hook.
func closedEnv(runDir string) map[string]string {
	return map[string]string{
		"OPENCODE_CONFIG_DIR":              filepath.Join(runDir, "config"),
		"XDG_CONFIG_HOME":                  filepath.Join(runDir, "xdg"),
		"OPENCODE_DISABLE_PROJECT_CONFIG":  "1",
		"OPENCODE_DISABLE_CLAUDE_CODE":     "1",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS": "1",
		"OPENCODE_DISABLE_AUTOUPDATE":      "1",
		"OPENCODE_DISABLE_MODELS_FETCH":    "1",
		"OPENCODE_DISABLE_LSP_DOWNLOAD":    "1",
	}
}

func TestGuard_WritesOnePluginAndTheClosedRunEnvironment(t *testing.T) {
	t.Parallel()

	launch := opencode.New().Guard("/bin/beehave check command", "/run")

	assert.Empty(t, launch.Args)
	assert.Equal(t, closedEnv("/run"), launch.Env)
	require.Len(t, launch.Files, 1)
	assert.Contains(t, launch.Files, pluginPath)
	assert.Contains(t, launch.Files[pluginPath], `"/bin/beehave check command"`)
	assert.Contains(t, launch.Files[pluginPath], "tool.execute.before")
}

func TestGuard_DoesNotLetOpenCodePureSwitchOffThePlugin(t *testing.T) {
	t.Parallel()

	assert.NotContains(t, opencode.New().Guard("g", "/run").Env, "OPENCODE_PURE")
}

func TestSandbox_CarriesTheSameClosedRunLaunchAsGuard(t *testing.T) {
	t.Parallel()

	adapter := opencode.New()

	assert.Equal(t, adapter.Guard("g", "/run"), adapter.Sandbox(agent.SandboxPlan{Work: "/w"}, "g", "/run"))
}

func TestSandbox_WithNoGuardStillClosesTheRunAndWritesNoPlugin(t *testing.T) {
	t.Parallel()

	launch := opencode.New().Sandbox(agent.SandboxPlan{Work: "/w"}, "", "/run")

	assert.Equal(t, closedEnv("/run"), launch.Env)
	assert.Empty(t, launch.Files)
}

func TestGuard_TheGuardCommandStaysOneJavaScriptString(t *testing.T) {
	t.Parallel()

	hostile := []string{
		`a"; process.exit(0); "`,
		"a$(touch /x)`id`",
		`a\"; throw 1; //`,
		"line\nbreak",
		"__GUARD_COMMAND__",
		"u v",
	}

	for _, command := range hostile {
		literal, err := json.Marshal(command)
		require.NoError(t, err)

		text := opencode.New().Guard(command, "/run").Files[pluginPath]

		assert.Contains(t, text, "const GUARD = "+string(literal)+"\n", command)
		// The rest of the plugin is the same as for a harmless command, so nothing left the string.
		plain := opencode.New().Guard("g", "/run").Files[pluginPath]
		assert.Equal(t,
			strings.Replace(plain, `const GUARD = "g"`, "const GUARD = "+string(literal), 1),
			text, command)
	}
}

func TestGuard_ThePluginFailsClosed(t *testing.T) {
	t.Parallel()

	text := opencode.New().Guard("g", "/run").Files[pluginPath]

	assert.Contains(t, text, `"beehave refused: "`)
	assert.Contains(t, text, "catch")
	assert.Contains(t, text, "=== 2")
}
