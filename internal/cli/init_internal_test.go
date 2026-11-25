package cli

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateFile_NeverReplacesAFileThatAppearsAfterTheCheck(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "workflow.star")
	require.NoError(t, os.WriteFile(path, []byte("mine\n"), 0o600))

	err := createFiles(&bytes.Buffer{}, []initFile{{path: path, data: []byte("init\n")}})

	require.Error(t, err)

	kept, readErr := os.ReadFile(path) //nolint:gosec // a test reads a file below its own temporary folder
	require.NoError(t, readErr)
	assert.Equal(t, "mine\n", string(kept))
}

func TestReadTemplate_HoldsEveryFileOfTheTemplateFolder(t *testing.T) {
	t.Parallel()

	var onDisk []string

	require.NoError(t, filepath.WalkDir(templateRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		rel, relErr := filepath.Rel(templateRoot, path)
		onDisk = append(onDisk, filepath.ToSlash(rel))

		return relErr
	}))

	var embedded []string

	for _, file := range readTemplate() {
		embedded = append(embedded, file.path)
	}

	sort.Strings(onDisk)
	sort.Strings(embedded)
	assert.Equal(t, onDisk, embedded, "a file that the embed leaves out is never written")
	assert.Len(t, embedded, 5)
}
