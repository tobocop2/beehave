package local_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const templateFolder = "../../internal/cli/initfiles"

// exampleFiles lists the workflow and the prompts of this folder, as paths below it.
func exampleFiles(t *testing.T) []string {
	t.Helper()

	files := []string{"workflow.star"}

	prompts, err := os.ReadDir("prompts")
	require.NoError(t, err)

	for _, prompt := range prompts {
		files = append(files, "prompts/"+prompt.Name())
	}

	sort.Strings(files)

	return files
}

func TestInitTemplate_HoldsTheWorkflowAndPromptsOfThisFolderByteForByte(t *testing.T) {
	t.Parallel()

	var copied []string

	require.NoError(t, filepath.WalkDir(templateFolder, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		rel, relErr := filepath.Rel(templateFolder, path)
		copied = append(copied, filepath.ToSlash(rel))

		return relErr
	}))
	sort.Strings(copied)

	assert.Equal(t, exampleFiles(t), copied, "the template of beehave init holds the same files as examples/local")

	for _, file := range copied {
		original, err := os.ReadFile(file) //nolint:gosec // a test reads files of its own folder
		require.NoError(t, err)

		template, err := os.ReadFile(filepath.Join(templateFolder, file)) //nolint:gosec // a test reads the template folder of the repository
		require.NoError(t, err)
		assert.Equal(t, string(original), string(template), "internal/cli/initfiles/%s differs from examples/local/%s: copy the example again", file, file)
	}
}
