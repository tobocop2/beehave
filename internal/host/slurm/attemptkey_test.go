package slurm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmit_ARetryOfAFailedJobUnderTheKeyOfTheNextTryStartsANewJob(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)

	first, err := h.Submit(context.Background(), keyed("run-1:4:1"))
	require.NoError(t, err)

	f.Jobs[jobNumber(t, first)].Final = "FAILED"

	second, err := h.Submit(context.Background(), keyed("run-1:4:2"))

	require.NoError(t, err)
	assert.NotEqual(t, first, second, "the second try is a new job")
	assert.Equal(t, 2, f.Sbatches)
}

func TestSubmit_ACrashAfterTheJobEndedFindsTheEndedJobAndRunsNothingAgain(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)

	first, err := h.Submit(context.Background(), keyed("run-1:4:1"))
	require.NoError(t, err)

	for _, state := range []string{"FAILED", "COMPLETED"} {
		f.Jobs[jobNumber(t, first)].Final = state

		again, err := h.Submit(context.Background(), keyed("run-1:4:1"))

		require.NoError(t, err)
		assert.Equal(t, first, again, "the job of the try that ended in %s is found by its key", state)
	}

	assert.Equal(t, 1, f.Sbatches)
}
