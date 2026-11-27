package opencode

import (
	_ "embed" // the plugin text
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/tobocop2/beehest/internal/agent"
)

const (
	pluginPath = "config/plugins/beehest-guard.js"
	// loadedPath is the marker that the plugin writes when it loads, inside the config folder of the run.
	loadedPath         = "config/beehest-guard-loaded"
	commandPlaceholder = "__GUARD_COMMAND__"
	loadedPlaceholder  = "__LOADED_PATH__"
)

//go:embed guardplugin.js
var pluginTemplate string

// Guard returns the launch that makes OpenCode run command before each bash call of one run: a plugin in the config folder of the run, which OPENCODE_CONFIG_DIR names.
// The plugin sends the command the call as OpenCode gives it, as DecodeHook reads it, and refuses the call when the command exits with code 2, and on any other failure of the command.
// The launch also closes the run: the project config, the Claude Code files and the external skills are off, no update or download runs,
// and XDG_CONFIG_HOME names an empty folder so that no global plugin of the owner loads. OPENCODE_PURE stays unset, because it may switch off the plugin.
// The plugin has the form of both plugin interfaces: OpenCode 1.x loads its server function and OpenCode 2.x loads its setup function, and each takes the hook of its own line.
// The plugin writes a marker file when it loads (Launch.Loaded); the runner sets Result.GuardMissing when the marker is missing at the end of the run.
// With an empty command the run stays open, and the launch only opens each file of write to the file tools (see openOutput); with no file either, it is empty.
// The skills list is not used: the launch closes the external skills, and OpenCode has no plugin of skills.
func (Adapter) Guard(command, runDir string, write, _ []string) agent.Launch {
	if command == "" {
		return openOutput(agent.Launch{}, write)
	}

	return openOutput(launch(command, runDir), write)
}

// Sandbox returns the launch of Guard. The profile has no sandbox, so the runner never asks; the answer keeps the run closed and guarded if it ever does.
func (Adapter) Sandbox(_ agent.SandboxPlan, guard, runDir string, write, _ []string) agent.Launch {
	return openOutput(launch(guard, runDir), write)
}

// configContent is the variable that holds a config for the run, over the config files.
const configContent = "OPENCODE_CONFIG_CONTENT"

// permissionConfig is the config that opens folders outside the work folder to the file tools. OPENCODE_PERMISSION is not used: the profile sets it for a step with no edits.
type permissionConfig struct {
	Permission struct {
		ExternalDirectory map[string]string `json:"external_directory"`
	} `json:"permission"`
}

// openOutput returns the launch with the folder of each file of write allowed for the file tools, as a pattern of the folder with a /* tail.
func openOutput(launch agent.Launch, write []string) agent.Launch {
	if len(write) == 0 {
		return launch
	}

	var config permissionConfig

	config.Permission.ExternalDirectory = map[string]string{}
	for _, file := range write {
		config.Permission.ExternalDirectory[filepath.Dir(file)+"/*"] = "allow"
	}

	raw, _ := json.Marshal(config) //nolint:errcheck,errchkjson // a struct of strings always encodes

	if launch.Env == nil {
		launch.Env = map[string]string{}
	}

	launch.Env[configContent] = string(raw)

	return launch
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
		result.Hooked = true
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
