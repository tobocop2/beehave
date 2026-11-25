package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	igate "github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

func TestProjectGate_StateReadsTheHeadTheTreeAndTheChangesOfTheWorktreeOfTheItem(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{status: " M README\n"}
	gate := projectGate{projects: localProject(), programs: programs{trees: treeAt("/tree"), local: gateRunner(calls, model.CommandResult{Stdout: "abc123\n"}, model.CommandResult{})}}

	dir, state, err := gate.State(t.Context(), "p", "1")

	require.NoError(t, err)
	assert.Equal(t, "/tree", dir)
	assert.Equal(t, igate.State{Head: "abc123", Tree: "tree-of-abc123"}, state, "a worktree with a change is not clean")
	assert.Equal(t, []string{"/tree", "/tree"}, calls.dirs, "both reads run in the worktree of the item")
}

func TestProjectGate_StateFailsWhenTheItemHasNoWorktreeOrTheReadFails(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		trees programTrees
		head  model.CommandResult
		want  string
	}{
		"no_folder":  {goneTree{}, model.CommandResult{Stdout: "abc123\n"}, "has no worktree, so the gate cannot read the commit its program judges"},
		"find_fails": {treeFails{}, model.CommandResult{Stdout: "abc123\n"}, "find the folder of item 1"},
		"git_fails":  {treeAt("/tree"), model.CommandResult{ExitCode: 128, Stderr: "not a repository"}, "not a repository"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gate := projectGate{projects: localProject(), programs: programs{trees: c.trees, local: gateRunner(&gateCalls{}, c.head, model.CommandResult{})}}

			_, _, err := gate.State(t.Context(), "p", "1")

			require.ErrorContains(t, err, c.want)
		})
	}
}
