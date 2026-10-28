package beadstest

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestReachable_GivesUpWhenNothingListensWithinTheWait(t *testing.T) {
	t.Parallel()

	// Nothing can listen on port 0, so no other test can take it between the pick and the dial.
	address := net.JoinHostPort("127.0.0.1", "0")

	assert.False(t, reachable(t.Context(), address, time.Millisecond))
}
