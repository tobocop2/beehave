// Package model holds the domain types and enums every other package shares.
package model

import (
	"errors"
	"fmt"
	"strings"
)

// Agent names an agent CLI that obee can start.
type Agent string

// The agents obee can start.
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
	TestReport  string
	PinFile     string
	BuildHost   string
	Host        *HostConfig
	Visibility  Visibility
	// Sandbox is the default for a step that sets no sandbox. SandboxRead and SandboxDeny extend the read base and the credential list of the engine.
	Sandbox     SandboxMode
	SandboxRead []string
	SandboxDeny []string
	// GateNetwork lists the domains that the gate command may reach, and GateAllowWrite the extra paths that it may write. The gate runs under the
	// same denied reads as an agent step. A gate on a build host runs outside the sandbox.
	GateNetwork    []string
	GateAllowWrite []string
}

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
