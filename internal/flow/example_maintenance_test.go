package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// publishEntry replaces the main function of the example with one that opens the pull request of a bump that the gate passed. The update and the gate need
// a worktree and a project that the test does not build, and the step of the pull request reads none of them.
const publishEntry = `
def main(item):
    job = struct(repo = "api", week = "2026-W44", fingerprint = "fp", base = "main", table = "t")
    ending = open_pull_request(job, 2)
    conclude(item, job, ending)
`

// maintenanceFlow is the text of examples/maintenance/bump.star with the main function of publishEntry in place of its own.
func maintenanceFlow(t *testing.T) string {
	t.Helper()

	text, err := os.ReadFile(filepath.Join("..", "..", "examples", "maintenance", "bump.star"))
	require.NoError(t, err)

	renamed := strings.Replace(string(text), "def main(item):", "def full_main(item):", 1)
	require.NotEqual(t, string(text), renamed, "the example has a main function")

	return renamed + publishEntry
}

func TestExampleMaintenance_ThePullRequestStepRunsUnderAProcessCapAndTheOtherScriptsRunUncapped(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"HOME": "/home/me"}
	h.writer = newFakeWriter()
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if strings.HasSuffix(request.Run[1], "pr.sh") {
			return CommandOutput{Stdout: "https://github.example/acme/pull/1\n"}, nil
		}

		return CommandOutput{}, nil
	}

	_, err := walkText(t, h, maintenanceFlow(t))

	require.NoError(t, err)
	require.Len(t, h.commands.requests, 1, "a bump that opened its pull request writes no log line")
	assert.True(t, strings.HasSuffix(h.commands.requests[0].Run[1], "/.upkeep/scripts/pr.sh"))
	assert.Equal(t, 512, h.commands.requests[0].MaxNewProcesses, "the push runs the git hooks of the repository under a cap")
}

func TestExampleMaintenance_AFailedPullRequestStepWritesTheLogLineWithNoCap(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"HOME": "/home/me"}
	h.writer = newFakeWriter()
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if strings.HasSuffix(request.Run[1], "pr.sh") {
			return CommandOutput{RC: 1, Stdout: "fork: retry: Resource temporarily unavailable\n"}, nil
		}

		return CommandOutput{}, nil
	}

	_, err := walkText(t, h, maintenanceFlow(t))

	require.NoError(t, err)
	require.Len(t, h.commands.requests, 2)
	assert.True(t, strings.HasSuffix(h.commands.requests[1].Run[1], "scripts/log.sh"))
	assert.Zero(t, h.commands.requests[1].MaxNewProcesses, "the log script is not capped")
	assert.Contains(t, h.commands.requests[1].Run, "error")
}
