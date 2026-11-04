package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultPrices_TheFileThatShipsWithBeehaveParses(t *testing.T) {
	t.Parallel()

	_, err := ParsePrices(priceData)

	require.NoError(t, err)
}
