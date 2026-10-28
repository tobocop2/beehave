package remedy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/remedy"
)

// readingBrief writes a brief that lists one file of the given size under its read-first heading, and returns the path of the brief.
func readingBrief(t *testing.T, size int) string {
	t.Helper()

	dir := t.TempDir()
	listed := filepath.Join(dir, "plan.md")

	require.NoError(t, os.WriteFile(listed, []byte(strings.Repeat("x", size)), 0o600))

	path := filepath.Join(dir, "brief.md")
	text := "## The item\n\nx\n\n## Read first\n\n- `" + listed + "` why\n\n## Names\n\n"

	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func TestSize_PrintsTheTokenTableAndPassesASmallReading(t *testing.T) {
	t.Parallel()

	path := readingBrief(t, 500)

	code, out, errOut := run(t, remedy.Deps{}, "size", path)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "TOTAL: about")
	assert.Contains(t, out, "OK: the required reading of "+path)
	assert.Empty(t, errOut)
}

func TestSize_AFileAboveTheFileLimitIsAWarningAndExitsOne(t *testing.T) {
	t.Parallel()

	path := readingBrief(t, 9000)

	code, out, _ := run(t, remedy.Deps{}, "size", path)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "OK: the required reading of "+path)
}

func TestSize_AReadingAboveTheLimitIsARefusal(t *testing.T) {
	t.Parallel()

	path := readingBrief(t, 170000)

	code, out, errOut := run(t, remedy.Deps{}, "size", path)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, out, "TOTAL: about")
	assert.Contains(t, errOut, "remedy size:")
	assert.NotContains(t, out, "OK: the required reading")
}

func TestSize_TheCapFlagSetsTheLaneCap(t *testing.T) {
	t.Parallel()

	path := readingBrief(t, 500)

	code, out, _ := run(t, remedy.Deps{}, "size", path, "--cap", "100000")

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "100000")

	code, _, errOut := run(t, remedy.Deps{}, "size", path, "--cap", "-5")

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, errOut, "remedy size:")
}

func TestSize_ABriefThatIsNotThereIsARefusal(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, remedy.Deps{}, "size", filepath.Join(t.TempDir(), "none.md"))

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "no brief at")
}

func TestSize_ABadCommandLineShowsTheUsageAndIsARefusal(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no brief":       {"size"},
		"unknown flag":   {"size", "b.md", "--bogus"},
		"extra argument": {"size", "b.md", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code, out, errOut := run(t, remedy.Deps{}, args...)

			assert.Equal(t, remedy.ExitRefused, code)
			assert.Empty(t, out)
			assert.Contains(t, errOut, "usage: remedy size <brief>")
		})
	}
}
