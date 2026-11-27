package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/sandbox"
)

// The git of the search path reads its program, its exec path, which holds upload-pack, and its libraries, each with its links resolved, and no system
// folder whole.
func TestFindGit_GivesTheProgramItsExecPathAndItsLibraries(t *testing.T) {
	t.Parallel()

	git, err := findGit(t.Context(), config.ProcessEnv()["PATH"])
	require.NoError(t, err)
	t.Logf("the read set of git on this machine: %q", git.reads)

	require.GreaterOrEqual(t, len(git.reads), 2)
	assert.Equal(t, git.binary, git.reads[0])
	assert.FileExists(t, filepath.Join(git.reads[1], "git-upload-pack"), "the second path is the exec path")

	for _, path := range git.reads {
		assert.True(t, filepath.IsAbs(path), path)
		assert.Equal(t, evaluated(t, path), path, "the links of %s are resolved", path)
		assert.NotContains(t, sandbox.SystemRead(), path, "no system folder whole")
	}
}

// A git that is missing, whose exec path cannot be read or found, or whose libraries cannot be read, gives no read set, so the copy does not start.
func TestFindGit_AGitWhoseReadSetCannotBeFoundIsAnError(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		script string
		want   string
	}{
		"no git on the search path":   {"", "find git"},
		"an exec path that fails":     {"#!/bin/sh\nexit 3\n", "find the exec path"},
		"an exec path that is absent": {"#!/bin/sh\necho /nonexistent/git-core\n", "find the exec path"},
		"a git that is no program":    {"#!/bin/sh\necho /\n", "find the libraries"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if c.script != "" {
				require.NoError(t, exectest.Write(filepath.Join(dir, "git"), c.script))
			}

			_, err := findGit(t.Context(), dir)
			require.ErrorContains(t, err, c.want)
		})
	}
}
