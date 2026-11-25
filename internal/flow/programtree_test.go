package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gatepkg "github.com/tobocop2/beehave/internal/gate"
)

// scriptedTree is a gate whose State gives the states in order, then the error. The other methods of the gate are not called.
type scriptedTree struct {
	gate
	states []gatepkg.State
	err    error
}

func (g *scriptedTree) State(context.Context, string, string) (string, gatepkg.State, error) {
	if len(g.states) == 0 {
		return "", gatepkg.State{}, g.err
	}

	state := g.states[0]
	g.states = g.states[1:]

	return "/w", state, nil
}

// countedRun is a program that returns out and err, and counts its runs.
type countedRun struct {
	out  CommandOutput
	err  error
	runs int
}

func (c *countedRun) Run(context.Context, CommandRequest) (CommandOutput, error) {
	c.runs++

	return c.out, c.err
}

func TestGate_AProgramGateReadsTheWorktreeBeforeAndAfterTheProgram(t *testing.T) {
	t.Parallel()

	clean := gatepkg.State{Head: "h1", Tree: "t1", Clean: true}
	cases := map[string]struct {
		tree     *scriptedTree
		run      *countedRun
		repo     bool
		want     string
		wantRuns int
		wantHead string
	}{
		"the_same_state_records_the_head":   {&scriptedTree{states: []gatepkg.State{clean, clean}}, &countedRun{}, true, "", 1, "h1"},
		"no_repository_reads_no_worktree":   {&scriptedTree{err: errFake}, &countedRun{}, false, "", 1, ""},
		"a_failed_read_runs_no_program":     {&scriptedTree{err: errFake}, &countedRun{}, true, errFake.Error(), 0, ""},
		"a_failed_program_reads_no_after":   {&scriptedTree{states: []gatepkg.State{clean}}, &countedRun{err: errFake}, true, errFake.Error(), 1, ""},
		"a_failed_read_after_ends_the_gate": {&scriptedTree{states: []gatepkg.State{clean}, err: errFake}, &countedRun{}, true, errFake.Error(), 1, "h1"},
		"a_dirty_worktree_runs_no_program":  {&scriptedTree{states: []gatepkg.State{{Head: "h1", Tree: "t1"}}}, &countedRun{}, true, "not committed", 0, ""},
		"a_changed_tree_is_refused":         {&scriptedTree{states: []gatepkg.State{clean, {Head: "h2", Tree: "t2", Clean: true}}}, &countedRun{}, true, "changed", 1, "h1"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			e := &Engine{deps: Deps{Gate: c.tree, Commands: c.run, Repository: func(string) bool { return c.repo }}}

			state, _, err := e.programOnTree(t.Context(), CommandRequest{Project: testProject, Item: testItem})

			if c.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, c.want)
			}

			assert.Equal(t, c.wantRuns, c.run.runs)
			assert.Equal(t, c.wantHead, state.Head)
		})
	}
}
