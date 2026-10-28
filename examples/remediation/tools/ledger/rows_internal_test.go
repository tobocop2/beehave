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

func TestList_AnEmptyFileHasNoRows(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "rows.tsv")
	require.NoError(t, os.WriteFile(path, nil, fileMode))

	rows, err := NewTable(path).List()
	require.NoError(t, err)
	assert.Empty(t, rows)
}
