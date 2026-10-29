package ledger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublish_AFileThatCannotBeReadOrWrittenIsARefusalThatNamesIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	unreadable := filepath.Join(dir, "rows.tsv")
	require.NoError(t, os.Mkdir(unreadable, 0o700))
	require.ErrorContains(t, NewTable(unreadable).Publish("a\n"), "cannot read "+unreadable)

	blocked := filepath.Join(dir, "blocked.tsv")
	require.NoError(t, os.Mkdir(blocked+publishSuffix, 0o700))
	require.ErrorContains(t, NewTable(blocked).Publish("a\n"), "cannot write "+blocked)
	assert.NoFileExists(t, blocked)
}

func TestLocks_AFolderAsTheLockIsRefused(t *testing.T) {
	t.Parallel()

	folder := filepath.Join(t.TempDir(), "d.tsv")
	require.NoError(t, os.Mkdir(folder+lockSuffix, 0o700))

	requireLockRefused(t, folder)
}

func TestLocks_AReadOnlyFileAsTheLockIsRefused(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root can write a mode 0400 file")
	}

	readOnly := filepath.Join(t.TempDir(), "ro.tsv")
	require.NoError(t, os.WriteFile(readOnly+lockSuffix, nil, 0o400))

	requireLockRefused(t, readOnly)
}

func requireLockRefused(t *testing.T, path string) {
	t.Helper()

	ran := false
	run := func() error { ran = true; return nil }

	require.ErrorContains(t, NewTable(path).Hold(run), "cannot lock "+path+lockSuffix, path)
	require.ErrorContains(t, NewTable(path).Share(run), "cannot lock "+path+lockSuffix, path)
	assert.False(t, ran, path)
}

func TestList_AnEmptyFileHasNoRows(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "rows.tsv")
	require.NoError(t, os.WriteFile(path, nil, fileMode))

	rows, err := NewTable(path).List()
	require.NoError(t, err)
	assert.Empty(t, rows)
}
