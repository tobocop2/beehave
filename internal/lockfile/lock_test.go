package lockfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/lockfile"
)

func TestOpen_CreatesTheFileAndLocksIt(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "a.lock")
	held := lockfile.Open(path)

	require.NoError(t, held.Lock())
	assert.FileExists(t, path)

	other := lockfile.Open(path)
	taken, err := other.TryLock()
	require.NoError(t, err)
	assert.False(t, taken)

	require.NoError(t, held.Unlock())
}

func TestOpen_RefusesAFolder(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "d.lock")
	require.NoError(t, os.Mkdir(path, 0o700))

	require.Error(t, lockfile.Open(path).Lock())
	require.Error(t, lockfile.Open(path).RLock())
}

func TestOpen_RefusesAReadOnlyFile(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root can write a mode 0400 file")
	}

	path := filepath.Join(t.TempDir(), "ro.lock")
	require.NoError(t, os.WriteFile(path, nil, 0o400))

	require.Error(t, lockfile.Open(path).Lock())
}
