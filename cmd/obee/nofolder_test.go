package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
)

func TestPrograms_AnItemWithNoWorktreeRunsTheProgramInAnEmptyFolderThatIsRemoved(t *testing.T) {
	t.Parallel()

	process, err := os.Getwd()
	require.NoError(t, err)

	runs := newPrograms(config.Config{}, treeAt(""), newAdapters(nil, nil))

	got, err := runs.Run(t.Context(), flow.CommandRequest{Run: []string{"sh", "-c", "pwd -P; ls -A | wc -l; touch marker"}})

	require.NoError(t, err)
	assert.Equal(t, 0, got.RC)

	lines := strings.Fields(got.Stdout)
	require.Len(t, lines, 2)

	ran, err := filepath.EvalSymlinks(process)
	require.NoError(t, err)
	assert.NotEqual(t, ran, lines[0], "the program must not run in the folder of the obee process")
	assert.Equal(t, "0", lines[1], "the folder starts empty")
	assert.NoDirExists(t, lines[0], "obee removes the folder after the program ends")
	assert.NoFileExists(t, filepath.Join(process, "marker"))
}

func TestPrograms_AProgramThatCannotGetItsEmptyFolderIsRefused(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	runs := newPrograms(config.Config{}, treeAt(""), newAdapters(nil, nil))

	_, err := runs.Run(t.Context(), flow.CommandRequest{Item: "7", Run: []string{"true"}})

	require.ErrorContains(t, err, "make an empty folder for the command of item 7")
}

func TestProjectGate_RefusesAnItemWithNoWorktree(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	gate := projectGate{projects: localProject(), programs: programs{trees: treeAt(""), local: gateRunner(calls, model.CommandResult{Stdout: "abc"}, model.CommandResult{})}}

	_, err := gate.Run(t.Context(), "p", "1")

	require.ErrorIs(t, err, errNoGateFolder)
	assert.Empty(t, calls.binaries, "the gate never runs in the folder of the obee process")
}
