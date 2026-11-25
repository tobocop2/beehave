// Package model holds the domain types and enums every other package shares.
package model

import "strings"

// Agent names an agent CLI that beehave can start. The agent package of each CLI exports its name.
type Agent string

// Runner is one agent login. It holds no token: Keychain names the entry that does.
type Runner struct {
	Name      string
	Agent     Agent
	ConfigDir string
	Keychain  string
}

// Project is one project profile, from projects/<name>.star. Forge is empty for a project with no forge.
// Source is a git URL or an absolute path, and is empty for a project that gets its clone URL from its forge.
type Project struct {
	Name    string
	Repo    string
	Source  string
	Tracker TrackerName
	Claims  ClaimsName
	Forge   ForgeName
	// Blocks are the config blocks of the tracker, the claim store and the forge that the project names, by block name. An adapter that declares a block reads its own by that name.
	// The block of the build host adapter is in Host.Block.
	Blocks map[string]*Block
	// Tools is the programs that the project needs on the path, git first: the registry computes it from the adapters the project names.
	Tools      []string
	BaseBranch string
	Gate       string
	// GateSteps is the ordered gate list: one named command for each step. A project sets Gate or GateSteps, never both.
	GateSteps  []GateSpec
	TestReport string
	// TestCommand is the command that runs the test suite alone. The new-tests check runs it on the base commit. Empty means the project sets none.
	TestCommand string
	// TestPaths are the globs of the test files. The revert run copies the added and changed ones of the head onto the base commit.
	TestPaths []string
	// Notify is the program and its arguments that the engine runs when a wait for the owner opens. Empty means no notice.
	Notify  []string
	PinFile string
	// GateProgram lists the repository paths that hold the program of the gate: files, and directories that end in a slash. Empty means the gate runs as it always did.
	GateProgram []string
	// PRLabel is the label that every pull request opened by the engine carries. The config fills it, so it is never empty for a loaded project.
	PRLabel   string
	BuildHost string
	// BuildHosts is every build host that a project with more than one declares, and BuildHost is the first: the default of a run.
	// It is empty when BuildHost is the only one. Every cluster shares the Host block.
	BuildHosts []string
	Host       *HostConfig
	Visibility Visibility
	// Sandbox is the default for a step that sets no sandbox. SandboxRead and SandboxDeny extend the read base and the credential list of the engine.
	Sandbox     SandboxMode
	SandboxRead []string
	SandboxDeny []string
	// GateNetwork lists the domains that the gate command may reach, and GateAllowWrite the extra paths that it may write. The gate runs under the
	// same denied reads as an agent step. A gate on a build host runs outside the sandbox.
	GateNetwork    []string
	GateAllowWrite []string
	// SandboxEnforcer is "auto" or the name of the enforcer that runs the gate under the sandbox. Empty means "auto".
	SandboxEnforcer string
	// MaxSpawnDepth is the deepest level of spawn below the run a user started. The config fills it, so it is 1 or more for a loaded project.
	MaxSpawnDepth int
	// SecurityAlerts is true when a merge must find no open security alert at the gated head. A forge that cannot read alerts refuses the start.
	SecurityAlerts bool
	// Credentials says where the engine reads the token of each role. A project with none runs its forge calls with the login of the owner.
	Credentials map[Role]Credential

	// Vars are the free settings of the workflow of the project, as the file wrote them. The engine reads no key and no value. The values are JSON values:
	// nil, bool, string, json.Number, []any and map[string]any. A project that sets none has nil.
	Vars map[string]any
	// RunnerSkillLoads maps the name of each runner of the config to whether its agent CLI has a line that loads a skill before the first turn. The engine fills it from the runner profiles.
	RunnerSkillLoads map[string]bool
}

// HasRepository reports whether the project has a git repository: it sets a source or a forge, and the forge gives the clone URL.
// A project with no repository runs its items in plain folders, and it cannot call gate, merge, preflight, pr_read or issue_assignees.
func (p Project) HasRepository() bool { return p.Source != "" || p.Forge != "" }

// ToolGit is the program that every project needs on the path. The tools of an adapter arrive with its registration.
const ToolGit = "git"

func joinNames[T ~string](items []T) string {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = string(item)
	}

	return strings.Join(names, ", ")
}
