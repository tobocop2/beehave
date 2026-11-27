package lease_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/lease"
)

func TestOpen_NoRowsIsRefused(t *testing.T) {
	t.Parallel()

	claims, err := lease.Open(nil)

	require.Error(t, err)
	assert.Nil(t, claims)
}
