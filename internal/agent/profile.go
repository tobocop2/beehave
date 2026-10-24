// Package agent starts coding-agent CLIs and reads their transcripts.
package agent

import "github.com/tobocop2/obee/internal/model"

// profile is the command line and the login variables of one agent CLI.
type profile struct {
	binary     string
	args       []string
	configVars []string
	tokenVar   string
}

// profileOf returns the profile of an agent. The prompt always goes on stdin.
func profileOf(agent model.Agent) (profile, bool) {
	switch agent {
	case model.AgentClaude:
		return profile{ //nolint:gosec // the token variable name, not a credential
			binary:     "claude",
			args:       []string{"-p", "--output-format", "stream-json", "--verbose"},
			configVars: []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"},
			tokenVar:   "CLAUDE_CODE_OAUTH_TOKEN",
		}, true
	case model.AgentOpenCode:
		return profile{}, false
	}

	return profile{}, false
}
