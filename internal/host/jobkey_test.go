package host_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/host"
)

func TestJobKey_ReturnsTheKeyThatWithJobKeyPutInTheContextAndNoneForAPlainContext(t *testing.T) {
	t.Parallel()

	assert.Empty(t, host.JobKey(t.Context()))
	assert.Equal(t, "run-1:4", host.JobKey(host.WithJobKey(t.Context(), "run-1:4")))
}
