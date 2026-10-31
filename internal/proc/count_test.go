package proc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProc makes a folder that looks like /proc: one folder for each process, and one folder for each thread under task.
func fakeProc(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	for _, thread := range []string{"1/task/1", "1/task/2", "1/task/3", "7/task/7", "9", "self"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, thread), 0o750))
	}

	require.NoError(t, os.WriteFile(filepath.Join(root, "5"), nil, 0o600))

	return root
}

func TestCountTasks_CountsEachThreadOfTheProcessesOfTheUser(t *testing.T) {
	t.Parallel()

	got, err := countTasks(fakeProc(t), os.Getuid())

	require.NoError(t, err)
	assert.Equal(t, 4, got, "three threads of 1, one of 7, none for 9 (it has no task folder), and nothing for names that are not a number")
}

func TestCountTasks_SkipsTheProcessesOfAnotherUser(t *testing.T) {
	t.Parallel()

	got, err := countTasks(fakeProc(t), os.Getuid()+1)

	require.NoError(t, err)
	assert.Zero(t, got)
}

func TestCountTasks_FailsWhenTheFolderCannotBeRead(t *testing.T) {
	t.Parallel()

	_, err := countTasks(filepath.Join(t.TempDir(), "missing"), os.Getuid())

	require.Error(t, err)
}
