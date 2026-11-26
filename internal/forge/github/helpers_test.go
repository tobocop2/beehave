package github_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/ghcontract"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

const repo = "owner/name"

// fixed builds a forge whose every call gets the same answer.
func fixed(t *testing.T, stdout string) (*github.Forge, *proctest.Scripted) {
	t.Helper()

	run := proctest.Always(model.CommandResult{Stdout: stdout})

	return newForge(t, repo, run), run
}

// failing builds a forge whose every call exits 1 with the stderr.
func failing(t *testing.T, stderr string) (*github.Forge, *proctest.Scripted) {
	t.Helper()

	run := proctest.Always(model.CommandResult{ExitCode: 1, Stderr: stderr})

	return newForge(t, repo, run), run
}

func toJSON(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return string(data)
}

type fields = map[string]any

// graph wraps pull request fields as a GraphQL answer.
func graph(t *testing.T, pull fields) string {
	t.Helper()

	return toJSON(t, fields{"data": fields{"repository": fields{"pullRequest": pull}}})
}

func commandResult(stdout string, exit int) model.CommandResult {
	return model.CommandResult{ExitCode: exit, Stdout: stdout}
}

// strict makes any runner refuse a field or a flag that the pinned gh refuses. A scripted runner refuses in place and keeps its record of calls; another runner is wrapped.
// Every test builds the forge over strict(run): TestForge_IsBuiltOverAStrictRunnerInEveryTest fails for a call of github.New that does not.
func strict(run proc.Runner) proc.Runner {
	if scripted, ok := run.(*proctest.Scripted); ok {
		return scripted.Strict(ghcontract.Check)
	}

	return proctest.Checked(run, ghcontract.Check)
}

// newForge builds a forge over the runner, made strict.
func newForge(t *testing.T, name string, run proc.Runner) *github.Forge {
	t.Helper()

	f, err := github.New(name, strict(run))
	require.NoError(t, err)

	return f
}
