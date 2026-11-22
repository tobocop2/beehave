package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
)

func TestPrograms_AnItemWithNoWorktreeRunsTheProgramInAnEmptyFolderThatIsRemoved(t *testing.T) {
	t.Parallel()

	process, err := os.Getwd()
	require.NoError(t, err)

	runs := newPrograms(config.Config{}, treeAt(""), cloneLock{}, newAdapters(nil, nil, nil), nil)

	got, err := runs.Run(t.Context(), flow.CommandRequest{Run: []string{"sh", "-c", "pwd -P; ls -A | wc -l; touch marker"}})

	require.NoError(t, err)
	assert.Equal(t, 0, got.RC)

	lines := strings.Fields(got.Stdout)
	require.Len(t, lines, 2)

	ran, err := filepath.EvalSymlinks(process)
	require.NoError(t, err)
	assert.NotEqual(t, ran, lines[0], "the program must not run in the folder of the beehave process")
	assert.Equal(t, "0", lines[1], "the folder starts empty")
	assert.NoDirExists(t, lines[0], "beehave removes the folder after the program ends")
	assert.NoFileExists(t, filepath.Join(process, "marker"))
}

func TestPrograms_AProgramThatCannotGetItsEmptyFolderIsRefused(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	runs := newPrograms(config.Config{}, treeAt(""), cloneLock{}, newAdapters(nil, nil, nil), nil)

	_, err := runs.Run(t.Context(), flow.CommandRequest{Item: "7", Run: []string{"true"}})

	require.ErrorContains(t, err, "make an empty folder for the command of item 7")
}

func TestProjectGate_RefusesAnItemWithNoWorktree(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	gate := projectGate{projects: localProject(), programs: programs{trees: treeAt(""), local: gateRunner(calls, model.CommandResult{Stdout: "abc"}, model.CommandResult{})}}

	_, err := gate.Run(t.Context(), "p", "1", "", flow.GateJob{})

	require.ErrorIs(t, err, errNoGateFolder)
	assert.Empty(t, calls.binaries, "the gate never runs in the folder of the beehave process")
}

// goneTree is a worktree that someone removed: Path finds no folder, and Ensure makes the worktree again in dir and keeps the start it was asked for.
type goneTree struct {
	dir    string
	starts *[]string
}

func (goneTree) Path(context.Context, string, string) (string, error) { return "", nil }

func (t goneTree) Ensure(_ context.Context, _, _, start, _ string) (string, error) {
	*t.starts = append(*t.starts, start)

	return t.dir, nil
}

func TestPrograms_ARequestWithATreeStartRunsInTheWorktreeMadeAgainAtThatCommitAndAnyOtherInAnEmptyFolder(t *testing.T) {
	t.Parallel()

	tree, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	var starts []string

	runs := newPrograms(config.Config{}, goneTree{dir: tree, starts: &starts}, cloneLock{}, newAdapters(nil, nil, nil), nil)

	// A round item of a project with no repository has no tree start, so it runs in an empty folder as any other item does.
	for _, request := range []flow.CommandRequest{{Item: roundItem, TreeStart: "abc123"}, {Item: roundItem}, {Item: "7"}, {Item: "bb-1"}} {
		request.Run = []string{"pwd", "-P"}
		got, err := runs.Run(t.Context(), request)

		require.NoError(t, err, request.Item)
		assert.Equal(t, request.TreeStart != "", strings.TrimSpace(got.Stdout) == tree, "item %s with start %q ran in %s", request.Item, request.TreeStart, got.Stdout)
	}

	assert.Equal(t, []string{"abc123"}, starts, "only the request with a tree start made the worktree again, at that commit")

	_, err = programs{trees: treeFails{}}.Run(t.Context(), flow.CommandRequest{Item: roundItem, TreeStart: "abc123", Run: []string{"x"}})
	require.ErrorIs(t, err, errFake, "a worktree that cannot be made again refuses the command")
}
