package claude

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/gotelemetry"
	"github.com/tobocop2/beehave/internal/model"
)

// minSandboxVersion is the oldest Claude Code that knows every key of the sandbox settings below. The newest key is allowUnsandboxedCommands
// as the docs describe it from v2.1.285 (a project could override a false of the user before). strictAllowlist needs v2.1.219 and the lock of
// the setting sources v2.1.246. The version in which failIfUnavailable first appeared is not stated in the docs.
const minSandboxVersion = "2.1.285"

// homeFolder is how the sandbox names the home folder of the owner. Everything under it is unreadable unless the plan allows it.
const homeFolder = "~/"

// subprocessScrub is the variable that makes the CLI strip credentials, by name or by value, from the environment of the subprocesses that it starts,
// such as the Bash tool. The CLI keeps the token for its own calls. The docs describe it; no real run has shown that the token is gone from `env`.
const subprocessScrub = "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"

// envFileVar names a file that the CLI sources in its shell after the scrub, so a variable that the scrub drops, such as GIT_CONFIG_GLOBAL, still reaches git.
const envFileVar = "CLAUDE_ENV_FILE"

// tempVar names the folder under which the CLI keeps its temporary files and gives the shell its TMPDIR, so the home folder of the shell is in it too.
const tempVar = "CLAUDE_CODE_TMPDIR"

// envFile is the launch file in the run folder that exports the git variables of the plan and the home folder of the shell.
const envFile = "sandbox-env.sh"

// homeLines returns the lines of the env file that give the shell a new, empty home folder of its own, named after the run folder, in its temporary folder, which
// the sandbox lets it write. The Go command keeps its telemetry and its build cache under the home folder, and the sandbox denies the home folder of the owner.
// The lines write the telemetry mode off where the Go command reads it and point XDG_CONFIG_HOME at the config folder of the home folder, so no go call of
// the shell starts a telemetry child that outlives the step.
func homeLines(runDir string) string {
	var lines strings.Builder

	lines.WriteString("export HOME=\"${TMPDIR:-/tmp}\"/" + model.ShellQuote("beehave-home-"+filepath.Base(runDir)) + "\nmkdir -p \"$HOME\"\n")
	lines.WriteString("export XDG_CONFIG_HOME=\"$HOME\"/" + model.ShellQuote(gotelemetry.ConfigFolder) + "\n")

	for _, file := range gotelemetry.ModeFiles() {
		lines.WriteString("mkdir -p \"$HOME\"/" + model.ShellQuote(filepath.Dir(file)) + "\n")
		lines.WriteString("printf %s " + model.ShellQuote(gotelemetry.Mode) + " > \"$HOME\"/" + model.ShellQuote(file) + "\n")
	}

	return lines.String()
}

// Tools that the settings deny by name. The sandbox covers the Bash tool; these two reach the network without it.
func webTools() []string { return []string{"WebFetch", "WebSearch"} }

// sandboxSettings is the settings of one sandboxed run: the sandbox, the permission rules, and the hook of the guard when the run has one.
type sandboxSettings struct {
	Attribution *attribution `json:"attribution,omitempty"`
	Hooks       *hookEvents  `json:"hooks,omitempty"`
	Sandbox     sandboxBlock `json:"sandbox"`
	Permissions permissions  `json:"permissions"`
}

type sandboxBlock struct {
	Enabled                  bool       `json:"enabled"`
	FailIfUnavailable        bool       `json:"failIfUnavailable"`
	AllowUnsandboxedCommands bool       `json:"allowUnsandboxedCommands"`
	Filesystem               filesystem `json:"filesystem"`
	Network                  network    `json:"network"`
}

type filesystem struct {
	DenyRead  []string `json:"denyRead"`
	AllowRead []string `json:"allowRead"`
	DenyWrite []string `json:"denyWrite"`
}

type network struct {
	AllowedDomains  []string `json:"allowedDomains"`
	StrictAllowlist bool     `json:"strictAllowlist"`
}

type permissions struct {
	BlockReadsOutsideWorkingDirectories bool     `json:"blockReadsOutsideWorkingDirectories"`
	Allow                               []string `json:"allow,omitempty"`
	Deny                                []string `json:"deny"`
}

// Sandbox returns the arguments that confine the shell of one run to the plan.
//
// Three flags close the files and servers that the sandbox does not cover. --setting-sources with an empty value loads no user, project or local
// settings file, so an excludedCommands entry or an allow rule in one of them has no effect. --strict-mcp-config with no --mcp-config loads no MCP
// server, and a local one runs with the access of the owner. --disallowedTools removes WebFetch and WebSearch, which ignore the domain list.
//
// One --settings JSON holds the rest. The base denies the home folder: in it, only the work folder, the shared git directories and the extra read
// paths are readable. The sandbox reads every path outside the home folder by default; there, the settings deny only the credential paths. The
// credential paths are denied again for read and write, in the sandbox and in the permission rules of the file tools (a path in the sandbox alone
// leaves the Read tool open, and the reverse leaves cat open).
// A permission path is written as //absolute. The settings never set excludedCommands, allowLocalBinding, allowAllUnixSockets or allowAppleEvents,
// and never put the shared git directory in allowWrite: a linked work tree gets it, and an explicit entry could open hooks and config.
// The settings set no allowWrite list. Each output file of the run gets the allow rules of outputRules, and Claude Code merges the Edit path into the
// write paths of the sandbox too, so the rules open the file to the shell and to the file tools. A plan that may edit gets one more Edit allow
// rule, of the work folder with a /** tail; the deny rules still win over it.
// The git variables of the plan reach the shell through the env file, the only file in the run folder. Its allowRead entry keeps it readable when the
// run folder is under the home folder.
// The skills of the run reach the CLI as one plugin folder in the run folder (--plugin-dir), since the empty setting sources load no skill of the runner.
// The plugin folder is also a working directory (--add-dir), so the agent reads the files of a skill; the settings deny its write.
func (Adapter) Sandbox(plan agent.SandboxPlan, guard, runDir string, write, skillFolders []string) agent.Launch {
	skills := skillsPlugin(skillFolders, runDir)
	plan = skills.confine(readOnlyCache(readOutput(plan, write)))
	envPath := filepath.Join(runDir, envFile)
	settings := withGuard(settingsOf(plan, envPath, write), guard)
	raw, _ := json.Marshal(settings) //nolint:errcheck,errchkjson // a struct of strings and booleans always encodes

	return agent.Launch{
		Args: slices.Concat(
			noSettingSources(), []string{"--strict-mcp-config", "--disallowedTools", "WebFetch", "WebSearch"},
			addDirs(plan.ReadDirs), skills.args, []string{"--settings", string(raw)},
		),
		Env:    map[string]string{subprocessScrub: "1", envFileVar: envPath, tempVar: plan.Temp},
		Files:  skills.withFiles(map[string]string{envFile: exports(plan.GitEnv()) + homeLines(runDir) + exports(goEnv(plan.ModuleCache))}),
		Copies: skills.copies,
		Hooked: guard != "",
	}
}

// readOutput returns the plan with each output file readable by the shell, and its folder a working directory.
func readOutput(plan agent.SandboxPlan, write []string) agent.SandboxPlan {
	plan.Read = slices.Concat(plan.Read, write)
	plan.ReadDirs = slices.Concat(plan.ReadDirs, outputDirs(write))

	return plan
}

// readOnlyCache returns the plan with its module cache readable by the shell and denied for write, in the sandbox and in the file tools.
func readOnlyCache(plan agent.SandboxPlan) agent.SandboxPlan {
	if plan.ModuleCache != "" {
		plan.Read = append(slices.Clone(plan.Read), plan.ModuleCache)
		plan.DenyWrite = append(slices.Clone(plan.DenyWrite), plan.ModuleCache)
	}

	return plan
}

// goEnv returns the Go variables that make the go command of the shell read the module cache with no network, or none for a plan with no cache.
// GOFLAGS is empty, so a -mod of the owner does not reach the shell.
func goEnv(cache string) map[string]string {
	if cache == "" {
		return nil
	}

	return map[string]string{"GOMODCACHE": cache, "GOPROXY": "off", "GOTOOLCHAIN": "local", "GOFLAGS": ""}
}

// addDirs returns the --add-dir flag with each folder, which the CLI takes as a working directory, or no argument for no folder.
func addDirs(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}

	return append([]string{"--add-dir"}, dirs...)
}

// exports returns the POSIX shell lines that export each variable, in name order, with each value quoted.
func exports(env map[string]string) string {
	var lines strings.Builder

	for _, name := range slices.Sorted(maps.Keys(env)) {
		lines.WriteString("export " + name + "=" + model.ShellQuote(env[name]) + "\n")
	}

	return lines.String()
}

// withGuard returns the settings with the hook that runs guard, or the settings as they are when guard is empty.
func withGuard(settings sandboxSettings, guard string) sandboxSettings {
	if guard != "" {
		hooks := guardHooks(guard)
		settings.Hooks = &hooks
	}

	return settings
}

// settingsOf returns the settings of the sandbox and of the permission rules for a plan, with no hooks. envPath is the env file. The settings allow reading it, and not the
// run folder, so a run folder under the home folder stays denied except for that file.
func settingsOf(plan agent.SandboxPlan, envPath string, write []string) sandboxSettings {
	return sandboxSettings{
		Attribution: noAttribution(),
		Sandbox: sandboxBlock{
			Enabled:           true,
			FailIfUnavailable: true,
			Filesystem: filesystem{
				DenyRead:  join([]string{homeFolder}, plan.Deny, plan.DenyPatterns),
				AllowRead: join([]string{plan.Work}, plan.GitDirs, plan.Read, []string{envPath}),
				DenyWrite: join(plan.Deny, plan.DenyPatterns, plan.DenyWrite, plan.DenyWritePatterns),
			},
			Network: network{AllowedDomains: join(plan.Network), StrictAllowlist: true},
		},
		Permissions: permissions{
			BlockReadsOutsideWorkingDirectories: true, Allow: slices.Concat(outputRules(write), workEditRule(plan)),
			Deny: slices.Concat(webTools(), fileRules(plan.Deny, plan.DenyPatterns), editRules(plan.DenyWrite), patternEditRules(plan.DenyWritePatterns)),
		},
	}
}

// fileRules returns the deny rules of the file tools: Read and Edit, for each credential path as //absolute with a /** tail, and for each pattern as //absolute.
// The CLI matches a pattern rule when the file is used, so a file that is created later is covered.
func fileRules(paths, patterns []string) []string {
	rules := make([]string, 0, len(paths)+len(patterns))

	for _, path := range paths {
		rules = append(rules, readEdit("//"+strings.TrimPrefix(strings.TrimSuffix(path, "/"), "/")+"/**")...)
	}

	for _, pattern := range patterns {
		rules = append(rules, readEdit("//"+strings.TrimPrefix(pattern, "/"))...)
	}

	return rules
}

// editRules returns the Edit deny rule of each path that stays readable, as //absolute with a /** tail.
func editRules(paths []string) []string {
	rules := make([]string, 0, len(paths))

	for _, path := range paths {
		rules = append(rules, "Edit(//"+strings.TrimPrefix(strings.TrimSuffix(path, "/"), "/")+"/**)")
	}

	return rules
}

// patternEditRules returns the Edit rule of each pattern or exact file, as //absolute with no tail.
func patternEditRules(patterns []string) []string {
	rules := make([]string, 0, len(patterns))

	for _, pattern := range patterns {
		rules = append(rules, "Edit(//"+strings.TrimPrefix(pattern, "/")+")")
	}

	return rules
}

// outputRules returns the Read and the Edit allow rule of each output file, by its exact path with no tail. In a sandbox, Claude Code merges the Edit rule into the
// write paths of the shell too. In every launch the pair opens the file to the file tools: a Write needs a Read of the file first.
func outputRules(files []string) []string {
	var rules []string

	for _, file := range files {
		rules = append(rules, "Read(//"+strings.TrimPrefix(file, "/")+")")
	}

	return slices.Concat(rules, patternEditRules(files))
}

// outputDirs returns the folder of each output file as a working directory of the run. The engine gives each call a folder that holds its output file
// alone, so the file tools can read that file before they write it, and the CLI still blocks the read of every other output file.
func outputDirs(files []string) []string {
	dirs := make([]string, 0, len(files))
	for _, file := range files {
		dirs = append(dirs, filepath.Dir(file))
	}

	return dirs
}

// workEditRule returns the Edit allow rule of the work folder, as //absolute with a /** tail, for a plan that may edit, or none.
// The CLI forces the permission mode default while the subprocess scrub is on, so this rule and not --permission-mode opens the file tools.
func workEditRule(plan agent.SandboxPlan) []string {
	if !plan.Edits {
		return nil
	}

	return editRules([]string{plan.Work})
}

func readEdit(pattern string) []string {
	return []string{"Read(" + pattern + ")", "Edit(" + pattern + ")"}
}

// join returns the lists one after another, as a list that is never nil, so that the JSON holds [] and never null.
func join(lists ...[]string) []string {
	if all := slices.Concat(lists...); all != nil {
		return all
	}

	return []string{}
}
