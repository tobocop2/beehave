package opencode

import (
	_ "embed" // the plugin text
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/tobocop2/beehave/internal/agent"
)

const (
	pluginPath = "config/plugins/beehave-guard.js"
	// loadedPath is the marker that the plugin writes when it loads, inside the config folder of the run.
	loadedPath         = "config/beehave-guard-loaded"
	commandPlaceholder = "__GUARD_COMMAND__"
	loadedPlaceholder  = "__LOADED_PATH__"
)

//go:embed guardplugin.js
var pluginTemplate string

// Guard returns the launch that makes OpenCode run command before each bash call of one run: a plugin in the config folder of the run, which OpenCODE_CONFIG_DIR names.
// The plugin refuses the call when the command exits with code 2, and on any other failure of the command.
// The launch also closes the run: the project config, the Claude Code files and the external skills are off, no update or download runs,
// and XDG_CONFIG_HOME names an empty folder so that no global plugin of the owner loads. OPENCODE_PURE stays unset, because it may switch off the plugin.
// The plugin loads through the plugin interface of the version that the profile pins (GuardMinVersion), and the runner refuses an older version.
// The plugin writes a marker file when it loads (Launch.Loaded); the runner sets Result.GuardMissing when the marker is missing at the end of the run.
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
		result.Files = map[string]string{pluginPath: plugin(command, filepath.Join(runDir, loadedPath))}
		result.Loaded = loadedPath
	}

	return result
}

// plugin returns the plugin text with command and the path of the marker as JavaScript strings. The encoder escapes the quote, the backslash and the line breaks,
// and one pass of the replacer does not read the inserted text again, so no command can leave the string or fill the other place.
func plugin(command, marker string) string {
	return strings.NewReplacer(commandPlaceholder, literal(command), loadedPlaceholder, literal(marker)).Replace(pluginTemplate)
}

// literal returns text as one JavaScript string.
func literal(text string) string {
	encoded, _ := json.Marshal(text) //nolint:errcheck,errchkjson // a string always encodes

	return string(encoded)
}
