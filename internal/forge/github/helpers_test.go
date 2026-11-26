package github_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge/github"
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

// listPages is the answer of gh api graphql --paginate --slurp for a list of the repository: one page for each slice of rows, each with the total of all rows.
func listPages(t *testing.T, pages ...[]fields) string {
	t.Helper()

	total := 0
	for _, page := range pages {
		total += len(page)
	}

	answer := make([]fields, len(pages))
	for i, page := range pages {
		answer[i] = fields{"data": fields{"repository": fields{"list": fields{"totalCount": total, "nodes": page}}}}
	}

	return toJSON(t, answer)
}

// labelNodes are the labels of a GraphQL pull request.
func labelNodes(names ...string) fields {
	nodes := make([]fields, len(names))
	for i, name := range names {
		nodes[i] = fields{"name": name}
	}

	return fields{"nodes": nodes}
}

func commandResult(stdout string, exit int) model.CommandResult {
	return model.CommandResult{ExitCode: exit, Stdout: stdout}
}

func newForge(t *testing.T, name string, run proc.Runner) *github.Forge {
	t.Helper()

	f, err := github.New(name, run)
	require.NoError(t, err)

	return f
}
