// Package opencode is the agent adapter for the OpenCode CLI.
// The limits that a step author must know are in docs/design/agent-runs.md.
package opencode

import (
	"path/filepath"
	"time"

	"github.com/tobocop2/beehest/internal/agent"
)

// pinnedVersion is the OpenCode version that the recorded output in testdata comes from, and the oldest version that the adapter supports.
const pinnedVersion = "1.19.5"

// firstUnsupported is the first OpenCode version that the adapter refuses: the 3.x line has no recording in testdata.
const firstUnsupported = "3.0.0"

// privateServerFrom is the first OpenCode version that runs through a shared background service unless told otherwise. The flag --standalone gives the run
// its own server, so the environment of the run (the login folder, the guard plugin) is the one that applies.
const privateServerFrom = "2.0.0"

// stallNote names the likely cause of a run that printed nothing until its limit. With --format json, OpenCode prints no error for a model over its usage quota and retries.
const stallNote = "OpenCode prints nothing and retries when the model is over its usage quota, which is the likely cause if the run printed no line. Check the quota of the model, or use a model with a key."

// Adapter is the OpenCode CLI.
type Adapter struct{}

// New returns the OpenCode adapter.
func New() Adapter { return Adapter{} }

// Profile returns the command line and the login variables of OpenCode.
// The prompt arrives on stdin and no message argument follows "run". The profile never passes --auto: without it OpenCode rejects a permission request
// in a run that cannot ask, so a path outside the work folder stays closed to the file tools. "bash" and "edit" need no approval.
// A step with edits = False sets the permission "edit" to deny in OPENCODE_PERMISSION. The shell stays open, as for Claude; forbid_commands is the check.
// The login and the session store are in the folder that XDG_DATA_HOME names, which is the config_dir of the runner.
// OpenCode has no flag for turns or dollars. The runner counts shell calls for the turn cap and sums the dollars of each step for the cost cap.
func (Adapter) Profile() agent.Profile {
	return agent.Profile{
		Binary:      "opencode",
		Args:        []string{"run", "--format", "json"},
		NoEditEnv:   map[string]string{"OPENCODE_PERMISSION": `{"edit":"deny"}`},
		ConfigVars:  []string{"XDG_DATA_HOME"},
		ModelFlag:   "--model",
		Resume:      func(id string) []string { return []string{"--session", id} },
		StreamsCost: true,

		VersionArgs:     []agent.VersionArgs{{From: privateServerFrom, Args: []string{"--standalone"}}},
		SupportedFrom:   pinnedVersion,
		SupportedBefore: firstUnsupported,
		GuardPosixOnly:  true,
		DefaultTimeout:  time.Hour,
		StallNote:       stallNote,
	}
}

// TranscriptPath returns <configDir>/opencode/session/<session>, a path that no file holds. OpenCode keeps every session in one database, so it has no file for
// one session. The check before a resume finds no file, so a start after a lost run begins again with the note of the lost run and does not resume.
func (Adapter) TranscriptPath(configDir, _, session string) string {
	return filepath.Join(configDir, "opencode", "session", session)
}
