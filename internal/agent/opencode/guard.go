package opencode

import (
	_ "embed" // the plugin text
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/tobocop2/beehave/internal/agent"
)

const (
	pluginPath         = "config/plugins/beehave-guard.js"
	commandPlaceholder = "__GUARD_COMMAND__"
)

//go:embed guardplugin.js
var pluginTemplate string

// Guard returns the launch that makes OpenCode run command before each bash call of one run: a plugin in the config folder of the run, which OpenCODE_CONFIG_DIR names.
// The plugin refuses the call when the command exits with code 2, and on any other failure of the command.
// The launch also closes the run: the project config, the Claude Code files and the external skills are off, no update or download runs,
// and XDG_CONFIG_HOME names an empty folder so that no global plugin of the owner loads. OPENCODE_PURE stays unset, because it may switch off the plugin.
// The plugin loads through the plugin interface of the version that the fixtures come from. Nothing in a run proves the load; a smoke run does.
func (Adapter) Guard(command, runDir string) agent.Launch {
	return launch(command, runDir)
}

// Sandbox returns the launch of Guard. The profile has no sandbox, so the runner never asks; the answer keeps the run closed and guarded if it ever does.
func (Adapter) Sandbox(_ agent.SandboxPlan, guard, runDir string) agent.Launch {
	return launch(guard, runDir)
}

// launch returns the closed-run environment, and the plugin when command is not empty.
func launch(command, runDir string) agent.Launch {
	result := agent.Launch{Env: map[string]string{
		"OPENCODE_CONFIG_DIR":              filepath.Join(runDir, "config"),
		"XDG_CONFIG_HOME":                  filepath.Join(runDir, "xdg"),
		"OPENCODE_DISABLE_PROJECT_CONFIG":  "1",
		"OPENCODE_DISABLE_CLAUDE_CODE":     "1",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS": "1",
		"OPENCODE_DISABLE_AUTOUPDATE":      "1",
		"OPENCODE_DISABLE_MODELS_FETCH":    "1",
		"OPENCODE_DISABLE_LSP_DOWNLOAD":    "1",
	}}

	if command != "" {
		result.Files = map[string]string{pluginPath: plugin(command)}
	}

	return result
}

// plugin returns the plugin text with command as one JavaScript string. The encoder escapes the quote, the backslash and the line breaks,
// and one replacement does not read the inserted text again, so no command can leave the string.
func plugin(command string) string {
	literal, _ := json.Marshal(command) //nolint:errcheck,errchkjson // a string always encodes

	return strings.Replace(pluginTemplate, commandPlaceholder, string(literal), 1)
}
