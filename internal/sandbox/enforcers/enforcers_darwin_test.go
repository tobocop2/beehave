//go:build darwin

package enforcers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/sandbox"
)

func TestAuto_PicksSeatbeltOnMacOS(t *testing.T) {
	t.Parallel()

	name, err := sandbox.Choose(Table(), Order(), "auto")

	require.NoError(t, err)
	assert.Equal(t, "seatbelt", name)
}
