// Package claude is the agent adapter for the Claude Code CLI.
package claude

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

// Name is the name the Claude Code agent is chosen by.
const Name model.Agent = "claude"

// Adapter is the Claude Code CLI.
type Adapter struct {
	// env is the environment of the caller, which the usage read passes to the CLI.
	env map[string]string
}

// New returns the Claude Code adapter. env is the environment of the caller, which the usage read passes to the CLI.
func New(env map[string]string) Adapter { return Adapter{env: env} }

// ReadUsage returns the usage of one runner, read through the CLI.
func (a Adapter) ReadUsage(ctx context.Context, runner model.Runner) model.Reading {
	return UsageReader{Base: a.env}.Read(ctx, runner)
}

// ConfigFix is the fix of the doctor check of the config folder: how a person logs in to the CLI in dir.
func ConfigFix(dir string) string {
	return "Log in to it: CLAUDE_CONFIG_DIR=" + dir + " claude"
}

// TokenFix is the fix of the doctor check of the Keychain: how a person stores the token of the CLI in the entry.
func TokenFix(entry string) string {
	return "Run `claude setup-token`, then: security add-generic-password -s " + entry + " -a $USER -w"
}

// Profile returns the command line and the login variables of Claude Code.
// Print mode cannot ask for permission, so a call that needs it is refused. "--allowedTools Bash" runs shell commands; the PreToolUse hook of Guard
// stays the check for the forbidden kinds. "--permission-mode acceptEdits" (EditArgs, left out for a step with edits = False) accepts the file tools
// inside the work folder and refuses them outside it, except in a sandboxed run, where the CLI forces the mode default and the Edit allow rule of the
// work folder opens the file tools (Sandbox). It does not fence Bash: a shell command can write outside the work folder unless forbid_commands refuses it.
// The prompt arrives on stdin, so the list after --allowedTools takes no prompt by mistake.
func (Adapter) Profile() agent.Profile {
	return agent.Profile{ //nolint:gosec // the token variable name, not a credential
		Binary:     "claude",
		Args:       []string{"-p", "--output-format", streamJSON, "--verbose", "--allowedTools", bashTool},
		EditArgs:   []string{"--permission-mode", "acceptEdits"},
		ConfigVars: []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"},
		ConfigHome: ".claude",
		TokenVar:   "CLAUDE_CODE_OAUTH_TOKEN",
		ModelFlag:  "--model",
		Resume:     func(id string) []string { return []string{"--resume", id} },
		SkillLine:  func(name string) string { return "/" + pluginName + ":" + name },
		TurnsFlag:  "--max-turns",
		BudgetFlag: "--max-budget-usd",
		Sandbox:    true,
		MinVersion: minSandboxVersion,
	}
}

// attribution is the attribution setting of Claude Code. An empty commit and pr and a false sessionUrl omit the Co-Authored-By trailer of commits, the
// attribution text of pull requests and the session link. Every Claude Code version reads this form; the value false needs v2.1.281.
type attribution struct {
	Commit     string `json:"commit"`
	PR         string `json:"pr"`
	SessionURL bool   `json:"sessionUrl"`
}

// noAttribution is the attribution setting that every launch carries: the commits of an agent have no trailer that names the CLI.
func noAttribution() *attribution { return &attribution{Commit: "", PR: "", SessionURL: false} }

// guardSettings is the settings of one run with no sandbox: the attribution, a PreToolUse hook on the Bash tool, and the allow rules of the output files.
type guardSettings struct {
	Attribution *attribution `json:"attribution,omitempty"`
	Hooks       *hookEvents  `json:"hooks,omitempty"`
	Permissions *allowOnly   `json:"permissions,omitempty"`
}

// allowOnly is the permission rules of a run with no sandbox: allow rules only.
type allowOnly struct {
	Allow []string `json:"allow"`
}

type hookEvents struct {
	PreToolUse []hookGroup `json:"PreToolUse"`
}

type hookGroup struct {
	Matcher string        `json:"matcher"`
	Hooks   []hookHandler `json:"hooks"`
}

type hookHandler struct {
	Type      string `json:"type"`
	Command   string `json:"command"`
	OnFailure string `json:"onFailure"`
}

// Guard returns `--settings` with inline JSON that adds a PreToolUse hook with matcher Bash for this run, when command is not empty.
// Claude Code merges hook entries across settings sources, so no settings file of the user changes.
// The hook gets the call as JSON on stdin and blocks the call when it exits with code 2.
// "onFailure":"block" makes any other failure of the hook (cannot start, other exit code, timeout) block the call too. Claude Code v2.1.295 is the first version that knows the key.
// Each file of write is opened to the file tools by the same rules and working directory as in the sandbox.
// The skills reach the CLI as the plugin folder of the sandbox does, so a run with no sandbox loads them too. Every launch carries noAttribution in the same settings.
func (Adapter) Guard(command, runDir string, write, skills []string) agent.Launch {
	settings := guardSettings{Attribution: noAttribution()}

	if command != "" {
		hooks := guardHooks(command)
		settings.Hooks = &hooks
	}

	if len(write) > 0 {
		settings.Permissions = &allowOnly{Allow: outputRules(write)}
	}

	plugin := skillsPlugin(skills, runDir)
	args := slices.Concat(addDirs(slices.Concat(outputDirs(write), plugin.readDirs())), plugin.args)

	raw, _ := json.Marshal(settings) //nolint:errcheck,errchkjson // a struct of strings always encodes
	args = append(args, "--settings", string(raw))

	return agent.Launch{Args: args, Files: plugin.files, Copies: plugin.copies}
}

// guardHooks is the PreToolUse hook on the Bash tool that runs command.
func guardHooks(command string) hookEvents {
	return hookEvents{PreToolUse: []hookGroup{{Matcher: bashTool, Hooks: []hookHandler{{Type: "command", Command: command, OnFailure: "block"}}}}}
}

// streamLine is the part of a stream-json line that names the session and holds the last message.
type streamLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
}

// Line reads the session id from the init line and the final message from the result line of the stream.
func (Adapter) Line(line string) (string, string) {
	parsed, err := model.Decode[streamLine]("the claude output", line)
	if err != nil {
		return "", ""
	}

	switch {
	case parsed.Type == "system" && parsed.Subtype == "init":
		return parsed.SessionID, ""
	case parsed.Type == "result":
		return parsed.SessionID, parsed.Result
	}

	return "", ""
}

// TranscriptPath returns <configDir>/projects/<dir with each other character as '-'>/<session>.jsonl.
func (Adapter) TranscriptPath(configDir, dir, session string) string {
	folder := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}

		return '-'
	}, dir)

	return filepath.Join(configDir, "projects", folder, session+".jsonl")
}
