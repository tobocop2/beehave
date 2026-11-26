package realtools_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/proc"
)

// streamLine is the part of a line of the stream-json output that the row reads.
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
}

// TestRealClaude_PrintModeStream makes one print-mode call with the flags of the adapter, capped at 0.05 USD and one turn, and reads each line of the answer as JSON.
// The call needs CLAUDE_CODE_OAUTH_TOKEN and the pinned claude on PATH.
func TestRealClaude_PrintModeStream(t *testing.T) {
	t.Parallel()
	requireSuite(t)

	if env("CLAUDE_CODE_OAUTH_TOKEN") == "" {
		t.Skip("CLAUDE_CODE_OAUTH_TOKEN is not set: this row makes one paid call of claude and needs the token")
	}

	run := proc.NewExecRunner("claude", t.TempDir())
	run.Timeout = 3 * time.Minute
	run.Env = []string{"DISABLE_AUTOUPDATER=1"}
	run.Stdin = "Reply with the single word ok."

	done := run.Run(t.Context(), []string{"-p", "--output-format", "stream-json", "--verbose", "--max-turns", "1", "--max-budget-usd", "0.05", "--allowedTools", "Bash"})
	require.Zero(t, done.ExitCode, "claude exited with %d: %s", done.ExitCode, done.Stderr)

	var last streamLine

	for _, line := range strings.Split(strings.TrimSpace(done.Stdout), "\n") {
		var row streamLine

		require.NoError(t, json.Unmarshal([]byte(line), &row), "a line of the stream is not JSON: %q", line)

		last = row
	}

	assert.Equal(t, "result", last.Type, "the stream does not end with a result line")
	assert.False(t, last.IsError, "the result line reports an error: %s", last.Subtype)
}
