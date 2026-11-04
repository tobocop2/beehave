package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/store"
)

func TestParseStatus_TakesThreeNamesAndNamesThemInTheError(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"open", "closed", "hold"} {
		status, err := store.ParseStatus(name)
		require.NoError(t, err)
		assert.Equal(t, store.Status(name), status)
	}

	for _, name := range []string{"", "in_progress", "Open", "done"} {
		_, err := store.ParseStatus(name)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), `Use "open", "closed" or "hold"`)
	}
}

func TestMarker_MatchesAWholeLineOnly(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "obee-key: run:1", store.Marker("run:1"))
	assert.Equal(t, "obee-key: run:1", store.WithMarker("", "run:1"))
	assert.Equal(t, "text\n\nobee-key: run:1", store.WithMarker("text", "run:1"))

	assert.True(t, store.HasMarker(store.WithMarker("text", "run:1"), "run:1"))
	assert.True(t, store.HasMarker("a\r\nobee-key: run:1 \nb", "run:1"))
	assert.False(t, store.HasMarker(store.WithMarker("text", "run:10"), "run:1"))
	assert.False(t, store.HasMarker("see obee-key: run:1 in the text", "run:1"))
}

func TestVisible_DropsTheMarkerLineAndTheBlankLinesBeforeIt(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "text", store.Visible(store.WithMarker("text", "run:1")))
	assert.Equal(t, "a\n\nb", store.Visible("a\n\nb\n\nobee-key: run:1"))
	assert.Empty(t, store.Visible(store.Marker("run:1")))
	assert.Equal(t, "see obee-key: x in text", store.Visible("see obee-key: x in text"))
}
