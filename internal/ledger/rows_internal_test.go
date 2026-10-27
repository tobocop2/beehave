package ledger

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithLock_AFileThatTheSystemCannotLockIsARefusalThatNamesIt(t *testing.T) {
	t.Parallel()

	file, err := os.Create(filepath.Join(t.TempDir(), "lock"))
	require.NoError(t, err)
	require.NoError(t, file.Close())

	ran := false
	err = withLock(file, syscall.LOCK_EX, func() error { ran = true; return nil })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot lock "+file.Name()+": ")
	assert.False(t, ran)
}

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

func TestList_AnEmptyFileHasNoRows(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "rows.tsv")
	require.NoError(t, os.WriteFile(path, nil, fileMode))

	rows, err := NewTable(path).List()
	require.NoError(t, err)
	assert.Empty(t, rows)
}
