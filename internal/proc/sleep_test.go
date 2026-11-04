package proc_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/proc"
)

func TestSleep_ReturnsNilAfterTheWait(t *testing.T) {
	t.Parallel()

	require.NoError(t, proc.Sleep(t.Context(), time.Millisecond))
}

func TestSleep_AStoppedContextEndsTheWaitWithACancelThatKeepsTheCause(t *testing.T) {
	t.Parallel()

	ctx, stop := context.WithCancelCause(t.Context())
	stopped := assert.AnError
	stop(stopped)

	err := proc.Sleep(ctx, time.Hour)

	require.ErrorIs(t, err, context.Canceled, "the library records a stopped run, not a failed one")
	require.ErrorIs(t, err, stopped)
}
