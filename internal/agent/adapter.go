// Package agent starts coding-agent CLIs. An Adapter supplies the profile of one CLI and reads its transcript.
package agent

import (
	"errors"

	"github.com/tobocop2/obee/internal/model"
)

// ErrUnreadable is the cause when a transcript file cannot be read.
var ErrUnreadable = errors.New("cannot read the transcript")

// Profile is the command line and the login variables of one agent CLI. The prompt always goes on stdin.
type Profile struct {
	Binary     string
	Args       []string
	ConfigVars []string
	TokenVar   string
}

// Adapter is one agent CLI. The runner calls Profile before it starts a run. The check calls the two readers on a finished transcript.
// An unreadable transcript is an error that wraps ErrUnreadable.
type Adapter interface {
	// Profile returns the command line and the login variables of the CLI.
	Profile() Profile
	// BashCalls returns the command of each Bash call in the transcript at path.
	BashCalls(path string) ([]model.Call, error)
	// SkillCalls returns the name of each Skill call in the transcript at path. With agentID, calls of other agents are skipped.
	SkillCalls(path, agentID string) ([]model.Call, error)
}
