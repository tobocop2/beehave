// Package claude is the agent adapter for the Claude Code CLI.
package claude

import (
	"path/filepath"
	"strings"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
)

// Adapter is the Claude Code CLI.
type Adapter struct{}

// New returns the Claude Code adapter.
func New() Adapter { return Adapter{} }

// Profile returns the command line and the login variables of Claude Code.
func (Adapter) Profile() agent.Profile {
	return agent.Profile{ //nolint:gosec // the token variable name, not a credential
		Binary:     "claude",
		Args:       []string{"-p", "--output-format", streamJSON, "--verbose"},
		ConfigVars: []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"},
		TokenVar:   "CLAUDE_CODE_OAUTH_TOKEN",
		ModelFlag:  "--model",
		ResumeFlag: "--resume",
	}
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
