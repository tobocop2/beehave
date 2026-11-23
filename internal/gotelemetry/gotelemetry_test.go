package gotelemetry_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/gotelemetry"
)

func TestOff_WritesTheModeOffAtEachPathTheGoCommandReads(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	require.NoError(t, gotelemetry.Off(home))

	assert.Equal(t, []string{".config/go/telemetry/mode", "Library/Application Support/go/telemetry/mode"}, gotelemetry.ModeFiles())

	for _, file := range gotelemetry.ModeFiles() {
		content, err := os.ReadFile(filepath.Join(home, file))
		require.NoError(t, err)
		assert.Equal(t, "off\n", string(content), file)
	}
}

func TestOff_ReturnsTheErrorOfAHomeItCannotWriteInto(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(home, nil, 0o600))

	assert.Error(t, gotelemetry.Off(home), "a home that is a file holds no folder")
}
