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

// Agents lists every agent in the order the messages show them.
func Agents() []Agent {
	return []Agent{AgentClaude, AgentOpenCode}
}

// ParseAgent returns the agent that text names.
func ParseAgent(text string) (Agent, error) {
	for _, agent := range Agents() {
		if string(agent) == text {
			return agent, nil
		}
	}

	return "", fmt.Errorf("%w %q: use one of %s", ErrUnknownAgent, text, joinNames(Agents()))
}

// Runner is one agent login. It holds no token: Keychain names the entry that does.
type Runner struct {
	Name      string
	Agent     Agent
	ConfigDir string
	Keychain  string
}

// Project is one project profile, from projects/<name>.toml.
type Project struct {
	Name       string
	Repo       string
	BaseBranch string
	Gate       string
	BuildHost  string
}

func joinNames[T ~string](items []T) string {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = string(item)
	}

	return strings.Join(names, ", ")
}
