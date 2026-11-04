package claude

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/tobocop2/obee/internal/agent"
)

// minSandboxVersion is the oldest Claude Code that knows every key of the sandbox settings below. The newest key is allowUnsandboxedCommands
// as the docs describe it from v2.1.285 (a project could override a false of the user before). strictAllowlist needs v2.1.219 and the lock of
// the setting sources v2.1.246. The version in which failIfUnavailable first appeared is not stated in the docs.
const minSandboxVersion = "2.1.285"

// homeFolder is how the sandbox names the home folder of the owner. Everything under it is unreadable unless the plan allows it.
const homeFolder = "~/"

// Tools that the settings deny by name. The sandbox covers the Bash tool; these two reach the network without it.
func webTools() []string { return []string{"WebFetch", "WebSearch"} }

// sandboxSettings is the settings of one sandboxed run: the sandbox, the permission rules, and the hook of the guard when the run has one.
type sandboxSettings struct {
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
	DenyRead   []string `json:"denyRead"`
	AllowRead  []string `json:"allowRead"`
	DenyWrite  []string `json:"denyWrite"`
	AllowWrite []string `json:"allowWrite"`
}

type network struct {
	AllowedDomains  []string `json:"allowedDomains"`
	StrictAllowlist bool     `json:"strictAllowlist"`
}

type permissions struct {
	BlockReadsOutsideWorkingDirectories bool     `json:"blockReadsOutsideWorkingDirectories"`
	Deny                                []string `json:"deny"`
}

// Sandbox returns the arguments that confine the shell of one run to the plan.
//
// Three flags close the files and servers that the sandbox does not cover. --setting-sources with an empty value loads no user, project or local
// settings file, so an excludedCommands entry or an allow rule in one of them has no effect. --strict-mcp-config with no --mcp-config loads no MCP
// server, and a local one runs with the access of the owner. --disallowedTools removes WebFetch and WebSearch, which ignore the domain list.
//
// One --settings JSON holds the rest. The base is deny by default: the home folder is unreadable, and only the work folder, the shared git
// directories and the extra read paths are readable. The credential paths are denied again for read and write, in the sandbox and in the
// permission rules of the file tools (a path in the sandbox alone leaves the Read tool open, and the reverse leaves cat open).
// A permission path is written as //absolute. The settings never set excludedCommands, allowLocalBinding, allowAllUnixSockets or allowAppleEvents,
// and never put the shared git directory in allowWrite: a linked work tree gets it, and an explicit entry could open hooks and config.
func (Adapter) Sandbox(plan agent.SandboxPlan, guard, _ string) agent.Launch {
	settings := settingsOf(plan)

	if guard != "" {
		hooks := guardHooks(guard)
		settings.Hooks = &hooks
	}

	raw, _ := json.Marshal(settings) //nolint:errcheck,errchkjson // a struct of strings and booleans always encodes

	return agent.Launch{Args: []string{
		"--setting-sources", "", "--strict-mcp-config",
		"--disallowedTools", "WebFetch", "WebSearch",
		"--settings", string(raw),
	}}
}

// settingsOf returns the settings of the sandbox and of the permission rules for a plan, with no hooks.
func settingsOf(plan agent.SandboxPlan) sandboxSettings {
	return sandboxSettings{
		Sandbox: sandboxBlock{
			Enabled:           true,
			FailIfUnavailable: true,
			Filesystem: filesystem{
				DenyRead:   join([]string{homeFolder}, plan.Deny, plan.DenyPatterns),
				AllowRead:  join([]string{plan.Work}, plan.GitDirs, plan.Read),
				DenyWrite:  join(plan.Deny, plan.DenyPatterns, plan.DenyWrite, plan.DenyWritePatterns),
				AllowWrite: join(plan.AllowWrite),
			},
			Network: network{AllowedDomains: join(plan.Network), StrictAllowlist: true},
		},
		Permissions: permissions{BlockReadsOutsideWorkingDirectories: true, Deny: slices.Concat(webTools(), fileRules(plan.Deny, plan.DenyPatterns), editRules(plan.DenyWrite), patternEditRules(plan.DenyWritePatterns))},
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

// patternEditRules returns the Edit deny rule of each pattern, as //absolute.
func patternEditRules(patterns []string) []string {
	rules := make([]string, 0, len(patterns))

	for _, pattern := range patterns {
		rules = append(rules, "Edit(//"+strings.TrimPrefix(pattern, "/")+")")
	}

	return rules
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
