package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultPrices_TheFileThatShipsWithObeeParses(t *testing.T) {
	t.Parallel()

	_, err := ParsePrices(priceData)

	require.NoError(t, err)
}
