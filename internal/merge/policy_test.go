package merge_test

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

func TestMerge_CIRefusalNamesTheFixOfTheForge(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"files": []any{map[string]any{"path": ".github/workflows/ci.yml"}}})
	w.ci = model.CIWriteNo
	req := request()
	req.CIFix = "FIX-LINE"
	result := w.run(req)

	assert.Equal(t, merge.NoWorkflowScope, result.Stop)
	assert.Equal(t, "        Run: FIX-LINE", last(result))
	assert.NotContains(t, all(result), "gh auth")

	w = newWorld(t).set(t, map[string]any{"files": []any{map[string]any{"path": ".github/workflows/ci.yml"}}})
	w.ci = model.CIWriteNo
	req.CIFix = ""
	result = w.run(req)

	assert.Equal(t, merge.NoWorkflowScope, result.Stop)
	assert.NotContains(t, all(result), "Run:", "a forge with no fix gives no command line")
}

func TestMerge_CIPathsComeFromTheRequest(t *testing.T) {
	t.Parallel()

	files := map[string]any{"files": []any{map[string]any{"path": ".gitlab-ci.yml"}}}
	req := request()
	req.CIPaths = []string{".gitlab-ci.yml"}

	w := newWorld(t).set(t, files)
	w.ci = model.CIWriteNo
	result := w.run(req)

	assert.Equal(t, merge.NoWorkflowScope, result.Stop, "a CI file of the request is guarded")
	assert.Contains(t, all(result), "touches .gitlab-ci.yml")

	w = newWorld(t).set(t, files)
	w.ci = model.CIWriteNo
	result = w.run(request())

	assert.Equal(t, merge.Done, result.Stop, "a path that the request does not list is not guarded")
	assert.False(t, w.called("CanWriteCI"))
}

func TestMergePolicyText_NamesNoForge(t *testing.T) {
	t.Parallel()

	here := os.DirFS(".")

	files, err := fs.Glob(here, "*.go")
	require.NoError(t, err)

	checked := 0

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		text, err := fs.ReadFile(here, file)
		require.NoError(t, err)

		checked++

		for number, line := range strings.Split(string(text), "\n") {
			for _, word := range []string{"gh", "GitHub", ".github/workflows"} {
				assert.False(t, containsWord(line, word), "%s:%d names the forge program or its files (%s): %s", file, number+1, word, line)
			}
		}
	}

	assert.Positive(t, checked, "the test read the files of the package")
}

// containsWord is true when line holds word with no letter or digit on either side.
func containsWord(line, word string) bool {
	for start := 0; ; {
		at := strings.Index(line[start:], word)
		if at < 0 {
			return false
		}

		from, to := start+at, start+at+len(word)
		before := from == 0 || !isWordByte(line[from-1])
		after := to == len(line) || !isWordByte(line[to])

		if before && after {
			return true
		}

		start = to
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
