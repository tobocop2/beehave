package beads

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadableID_RefusesWhatIsMissing(t *testing.T) {
	t.Parallel()

	id, err := readableID("i")
	require.NoError(t, err)
	require.Equal(t, "i", id)

	_, err = readableID("")
	require.ErrorContains(t, err, "no readable id")
}
