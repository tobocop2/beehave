package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
)

// fakeXcodeSelect puts an xcode-select first on the PATH that runs script, and returns the PATH.
func fakeXcodeSelect(t *testing.T, script string) string {
	t.Helper()

	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "xcode-select"), "#!/bin/sh\n"+script+"\n"))
	t.Setenv("PATH", bin+":/usr/bin:/bin")

	return bin
}

func TestDeveloperFolder_ReturnsTheFolderThatXcodeSelectNames(t *testing.T) { //nolint:paralleltest // the test sets PATH
	folder := t.TempDir()
	fakeXcodeSelect(t, "echo "+folder)

	assert.Equal(t, folder, developerFolder(nil))
}

// The fake prints the DEVELOPER_DIR that it got, the way xcode-select honors it.
func TestDeveloperFolder_UsesTheEnvironmentOfTheEngineAndNotTheOneOfTheProcessOfTheCaller(t *testing.T) {
	owner, other := t.TempDir(), t.TempDir()
	fakeXcodeSelect(t, `echo "$DEVELOPER_DIR"`)
	t.Setenv("DEVELOPER_DIR", other)

	assert.Equal(t, owner, developerFolder([]string{"DEVELOPER_DIR=" + owner}))
}

func TestDeveloperFolder_ReturnsNothingWhenXcodeSelectFailsOrNamesNoFolder(t *testing.T) { //nolint:paralleltest // the test sets PATH
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, exectest.Write(file, "x"))

	for name, script := range map[string]string{
		"fails":        "echo " + t.TempDir() + "; exit 2",
		"missing":      "echo /beehest/absent/folder",
		"relative":     "echo relative/folder",
		"a file":       "echo " + file,
		"prints empty": "true",
	} {
		fakeXcodeSelect(t, script)
		assert.Empty(t, developerFolder(nil), name)
	}
}

// An xcode-select that outlasts the limit is killed, and the engine finds no folder.
func TestDeveloperFolder_ReturnsNothingWhenXcodeSelectOutlastsTheLimit(t *testing.T) { //nolint:paralleltest // the test sets PATH
	fakeXcodeSelect(t, "sleep 5; echo "+t.TempDir())

	start := time.Now()

	assert.Empty(t, developerFolderWithin(nil, 200*time.Millisecond))
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestDeveloperFolder_ReturnsNothingWithNoXcodeSelectOnThePath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	assert.Empty(t, developerFolder(nil))
}
