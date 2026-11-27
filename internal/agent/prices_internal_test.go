package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultPrices_TheFileThatShipsWithBeehestParses(t *testing.T) {
	t.Parallel()

	_, err := ParsePrices(priceData)

	require.NoError(t, err)
}
