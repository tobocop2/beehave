// Package opencode is the agent adapter for the OpenCode CLI.
//
// Limits that a step author must know:
//   - OpenCode shows a tool call on its output when the call ends, not when it starts, so the turn cap counts a shell call one call late.
//   - OpenCode has no sandbox. The profile has none, so a step with a sandbox does not start; set sandbox = None on the step.
//   - OpenCode reports no skill calls, so a step with require_skills does not start.
//   - The session lives in the login folder of the runner. A resume works on the runner that made the session only.
//   - If the guard plugin does not load, OpenCode runs the shell commands with no check and exits with code 0. A step with forbid_commands does not start on a version
//     older than the pinned one, and a run whose plugin left no marker file ends with a violation line.
//   - The guard needs a POSIX shell, so a step with forbid_commands does not start on Windows.
//   - A run that sets no timeout ends after one hour. A model over its free quota prints nothing and retries until it is killed; the note of the timed-out run names that cause.
//   - A cost cap is checked on the dollars of finished steps, so a run can pass the cap by the cost of one step.
package opencode

import (
	"path/filepath"
	"time"

	"github.com/tobocop2/beehave/internal/agent"
)

// pinnedVersion is the OpenCode version that the recorded output in testdata comes from, and the oldest version whose plugin interface is known to load the guard.
const pinnedVersion = "1.19.5"

// stallNote names the likely cause of a run that printed nothing until its limit. With --format json, OpenCode prints no error for a model over its usage quota and retries.
const stallNote = "OpenCode prints nothing and retries when the model is over its usage quota, which is the likely cause if the run printed no line. " +
	"Check the quota of the model, or use a model with a key."

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

		MinVersion:      pinnedVersion,
		GuardMinVersion: pinnedVersion,
		GuardPosixOnly:  true,
		NoSkillCalls:    true,
		DefaultTimeout:  time.Hour,
		StallNote:       stallNote,
	}
}

// TranscriptPath returns <configDir>/opencode/opencode.db, the database that holds every session. Nothing reads it.
func (Adapter) TranscriptPath(configDir, _, _ string) string {
	return filepath.Join(configDir, "opencode", "opencode.db")
}
