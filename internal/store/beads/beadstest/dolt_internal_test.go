package beadstest

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestReachable_GivesUpWhenNothingListensWithinTheWait(t *testing.T) {
	t.Parallel()

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(FreePort(t)))

	assert.False(t, reachable(t.Context(), address, time.Millisecond))
}
