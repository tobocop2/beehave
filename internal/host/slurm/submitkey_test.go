package slurm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/model"
)

func keyed(key string) model.JobSpec {
	return model.JobSpec{Name: "gate", Command: hosttest.PassingCommand, Key: key}
}

func TestSubmit_ASubmitRetriedAfterACrashFindsTheJobThatSlurmAlreadyQueued(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)

	first, err := h.Submit(context.Background(), keyed("run-1:4"))
	require.NoError(t, err)

	// The engine crashed before it recorded the id. The replay submits again with the same key.
	second, err := h.Submit(context.Background(), keyed("run-1:4"))

	require.NoError(t, err)
	assert.Equal(t, first, second, "the retry returns the job that the first submit queued")
	assert.Equal(t, 1, f.Sbatches, "Slurm queued the job once")
}

func TestSubmit_ASubmitRetriedAfterSlurmForgotTheJobFindsItInAccounting(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)

	first, err := h.Submit(context.Background(), keyed("run-1:4"))
	require.NoError(t, err)

	f.Jobs[jobNumber(t, first)].Forgot = true

	second, err := h.Submit(context.Background(), keyed("run-1:4"))

	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, f.Sbatches)
}

func TestSubmit_TwoKeysAndNoKeyQueueSeparateJobs(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)

	for _, spec := range []model.JobSpec{keyed("run-1:4"), keyed("run-1:5"), keyed(""), keyed("")} {
		_, err := h.Submit(context.Background(), spec)
		require.NoError(t, err)
	}

	assert.Equal(t, 4, f.Sbatches)
}

func TestSubmit_ACanceledJobOfTheKeyDoesNotCountAndTheRetryQueuesANewOne(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)

	first, err := h.Submit(context.Background(), keyed("run-1:4"))
	require.NoError(t, err)

	f.Jobs[jobNumber(t, first)].Canceled = true

	second, err := h.Submit(context.Background(), keyed("run-1:4"))

	require.NoError(t, err)
	assert.NotEqual(t, jobNumber(t, first), jobNumber(t, second))
	assert.Equal(t, 2, f.Sbatches)
}

func TestSubmit_ALookupThatFailsQueuesNothing(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		match string
		drops bool
	}{
		{name: "squeue_fails", match: "squeue"},
		{name: "sacct_fails", match: "sacct"},
		{name: "link_drops_on_squeue", match: "squeue", drops: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h, f, _ := newClockedHost(t)

			if tc.drops {
				f.DropAll = tc.match
			} else {
				f.BadLookup = tc.match
			}

			_, err := h.Submit(context.Background(), keyed("run-1:4"))

			require.Error(t, err)
			assert.Zero(t, f.Sbatches, "a cluster that cannot say whether the job exists gets no second copy")
		})
	}
}
