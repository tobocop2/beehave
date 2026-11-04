// Package model holds the domain types and enums every other package shares.
package model

import (
	"errors"
	"fmt"
	"strings"
)

// Agent names an agent CLI that beehave can start.
type Agent string

// The agents beehave can start.
const (
	AgentClaude   Agent = "claude"
	AgentOpenCode Agent = "opencode"
)

// ErrUnknownAgent is returned when text names no known agent.
var ErrUnknownAgent = errors.New("unknown agent")

// agents lists every agent in the order the messages show them.
func agents() []Agent {
	return []Agent{AgentClaude, AgentOpenCode}
}

// ParseAgent returns the agent that text names.
func ParseAgent(text string) (Agent, error) {
	for _, agent := range agents() {
		if string(agent) == text {
			return agent, nil
		}
	}

	return "", fmt.Errorf("%w %q: use one of %s", ErrUnknownAgent, text, joinNames(agents()))
}

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
	Name        string
	Repo        string
	Source      string
	Tracker     TrackerName
	Claims      ClaimsName
	Forge       ForgeName
	BeadsPrefix string
	BaseBranch  string
	Gate        string
	// GateSteps is the ordered gate list: one named command for each step. A project sets Gate or GateSteps, never both.
	GateSteps  []GateSpec
	TestReport string
	PinFile    string
	// Changelog is the path of the changelog file of the repository, or empty when the project declares none. It is a fact for the workflow, which refuses a merge that leaves it unchanged.
	Changelog string
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

	// The facts of the repository for the agents of a workflow. Each one is empty when the project file leaves it out, and the `facts` builtin hands them to the workflow.
	// People maps a role to a login, for example owner, and the hours of idle time after which a pull request of a third party counts as stale. Epics maps a queue to its epic.
	People map[string]string
	Epics  map[string]string
	// GateFiles are the workflow files that the list of gate commands comes from, and Conventions the files to review against.
	GateFiles   []string
	Conventions []string
	// BriefRules, StackedRules, Limits, Traps and TokenRules are the rules for the brief, for a pull request that stacks on another, the hard limits with the commands never run
	// and the real user data, the measured traps with the ownership rules, and the token discipline with the cross-repository and release steps.
	BriefRules   []string
	StackedRules []string
	Limits       []string
	Traps        []string
	TokenRules   []string
	// Board is the GitHub Project that a workflow keeps in step with the item. The zero value means the project has no board.
	Board Board
	// Schedules are the cron schedules that start an item run. The file order is kept.
	Schedules []Schedule
}

// Schedule starts a run of Workflow on a new item each time Cron fires. Cron has seconds, or is a descriptor such as @weekly.
// Workflow is an absolute path. Timezone names an IANA zone. Item is the prefix of the item id. CatchUp asks for the ticks that an engine missed.
type Schedule struct {
	Name     string
	Cron     string
	Workflow string
	Item     string
	Timezone string
	CatchUp  bool
}

// Board names a GitHub Project board and the two status columns that a workflow sets. Owner is empty when the owner of the repository owns the board.
type Board struct {
	Owner   string
	Number  int
	Started string
	Merged  string
}

// HasRepository reports whether the project has a git repository: it sets a source or a forge, and the forge gives the clone URL.
// A project with no repository runs its items in plain folders, and it cannot call gate, merge, batch_merge or preflight.
func (p Project) HasRepository() bool { return p.Source != "" || p.Forge != "" }

// The programs that the adapters of a project start.
const (
	ToolGit = "git"
	ToolBd  = "bd"
	ToolGh  = "gh"
)

// Tools returns the programs that the project needs on the path: git always, bd for a beads tracker or beads claims, and gh for a GitHub tracker or forge.
func (p Project) Tools() []string {
	tools := []string{ToolGit}

	if p.Tracker == TrackerBeads || p.Claims == ClaimsBeads {
		tools = append(tools, ToolBd)
	}

	if p.Tracker == TrackerGithub || p.Forge == ForgeGithub {
		tools = append(tools, ToolGh)
	}

	return tools
}

func joinNames[T ~string](items []T) string {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = string(item)
	}

	return strings.Join(names, ", ")
}
