package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireReplacedNotTruncated fails when write changed the old file in place. A write that renames a new file over the target leaves a second
// name of the old file with the old text; a write that truncates the target changes both names, and a crash in between loses the old text.
func requireReplacedNotTruncated(t *testing.T, path string, write func(t *testing.T)) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte("old"), ownerMode))

	alias := path + ".alias"
	require.NoError(t, os.Link(path, alias))

	write(t)

	kept, err := os.ReadFile(alias) //nolint:gosec // a path in the test folder
	require.NoError(t, err)
	assert.Equal(t, "old", string(kept), "the write truncated the file in place instead of replacing it")

	now, err := os.ReadFile(path) //nolint:gosec // a path in the test folder
	require.NoError(t, err)
	assert.NotEqual(t, "old", string(now), "the write did not reach the file")
}

func TestSaveOwner_ReplacesTheFileInsteadOfTruncatingIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ownersDir), ownersMode))

	requireReplacedNotTruncated(t, ownerPath(dir, "r1"), func(t *testing.T) {
		t.Helper()
		require.NoError(t, saveOwner(dir, "r1", "fresh"))
	})
}

func TestGroups_WriteReplacesTheFileInsteadOfTruncatingIt(t *testing.T) {
	t.Parallel()

	g := newGroups(t)
	g.rows = []groupRow{{pgid: 42, started: "t0"}}

	requireReplacedNotTruncated(t, g.path, func(t *testing.T) {
		t.Helper()
		require.NoError(t, g.write())
	})
}

func TestRecordSchedule_ReplacesTheFileInsteadOfTruncatingIt(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	requireReplacedNotTruncated(t, pollScheduleFile(engine.dir), func(t *testing.T) {
		t.Helper()
		engine.recordSchedule(errFake)
	})
}
