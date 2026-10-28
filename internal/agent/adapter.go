// Package agent starts coding-agent CLIs. An Adapter supplies the profile of one CLI and reads its transcript.
package agent

import (
	"errors"

	"github.com/tobocop2/obee/internal/model"
)

// ErrUnreadable is the cause when a transcript file cannot be read.
var ErrUnreadable = errors.New("cannot read the transcript")

// Profile is the command line and the login variables of one agent CLI. The prompt always goes on stdin.
// ModelFlag and ResumeFlag are the flags that take a model name and a session id. An empty flag means the CLI has no such option.
type Profile struct {
	Binary     string
	Args       []string
	ConfigVars []string
	TokenVar   string
	ModelFlag  string
	ResumeFlag string
}

// Adapter is one agent CLI. The runner calls Profile before it starts a run. The agent step calls the two readers on a finished transcript: SkillCalls always, and BashCalls when the step sets `forbid_commands`.
// Every adapter must implement both. An unreadable transcript is an error that wraps ErrUnreadable.
// Call order for one run: Profile before the CLI starts, Line for each output line while it runs, then TranscriptPath, SkillCalls and BashCalls after it ends.
// Line never sees a line of an earlier run, and TranscriptPath needs the session id that Line returned.
type Adapter interface {
	// Profile returns the command line and the login variables of the CLI.
	Profile() Profile
	// Line reads one line of the output of the CLI. It returns the session id and the message text that the line holds, or empty strings.
	Line(line string) (session, text string)
	// TranscriptPath returns where the CLI keeps the transcript of a session that ran in dir with the login folder configDir.
	TranscriptPath(configDir, dir, session string) string
	// BashCalls returns the command of each Bash call in the transcript at path.
	BashCalls(path string) ([]model.Call, error)
	// SkillCalls returns the name of each Skill call in the transcript at path. With agentID, calls of other agents are skipped.
	SkillCalls(path, agentID string) ([]model.Call, error)
}
