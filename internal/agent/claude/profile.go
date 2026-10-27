// Package claude is the agent adapter for the Claude Code CLI.
package claude

import "github.com/tobocop2/obee/internal/agent"

// Adapter is the Claude Code CLI.
type Adapter struct{}

// New returns the Claude Code adapter.
func New() Adapter { return Adapter{} }

// Profile returns the command line and the login variables of Claude Code.
func (Adapter) Profile() agent.Profile {
	return agent.Profile{ //nolint:gosec // the token variable name, not a credential
		Binary:     "claude",
		Args:       []string{"-p", "--output-format", "stream-json", "--verbose"},
		ConfigVars: []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"},
		TokenVar:   "CLAUDE_CODE_OAUTH_TOKEN",
	}
}
