package slurm

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/slurmtest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// jobNumber is the Slurm job number inside the job id that Submit returned.
func jobNumber(t *testing.T, id model.JobID) string {
	t.Helper()

	job, err := parseJobID(id)
	require.NoError(t, err)

	return job.ID
}

func TestSubmit_WrapsTheCommandInTheSlotLoopOnlyForAJobWithASlot(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)

	limited, err := h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: "echo hi", Slot: model.Slot{Class: "mutation", Count: 3}})
	require.NoError(t, err)

	free, err := h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: "echo hi"})
	require.NoError(t, err)

	text := f.Jobs[jobNumber(t, limited)].Command
	assert.Contains(t, text, "flock -n 9")
	assert.Contains(t, text, "-le 3")
	assert.Contains(t, text, ".beehave/slots/mutation")
	assert.Contains(t, text, `.beehave/waiting-$SLURM_JOB_ID`)
	assert.Equal(t, "echo hi", f.Jobs[jobNumber(t, free)].Command)
}

func TestSubmit_RefusesASlotWithABadClassOrNoCountAndQueuesNothing(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)

	_, err := h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: "echo hi", Slot: model.Slot{Class: "a b", Count: 1}})

	require.ErrorIs(t, err, model.ErrBadSlot)
	assert.Zero(t, f.Sbatches)
}

// A pending job does not count toward the stale limit. The job reads RUNNING in squeue for far longer than the limit in both cases;
// the marker in the login folder is the only difference.
func TestWait_AWaitingMarkerReadsPendingSoTheJobNeverGoesStale(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		marker bool
		want   model.JobState
	}{
		"a job with the marker waits for its slot and ends as it ends": {marker: true, want: model.JobSucceeded},
		"a job with no marker and no log growth goes stale":            {marker: false, want: model.JobStale},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, f, _ := newClockedHost(t)
			id, job := submitted(t, h, f)
			job.Live, job.LogSizes = 300, []int{10}
			f.Files[".beehave/waiting-"+jobNumber(t, id)] = tc.marker

			state, err := h.Wait(context.Background(), id)

			require.NoError(t, err)
			assert.Equal(t, tc.want, state)
		})
	}
}

// testExit answers the read of the marker with an exit status, and every other call as the fake cluster does.
type testExit struct {
	inner proc.Runner
	code  int
}

func (r testExit) Run(ctx context.Context, args []string) model.CommandResult {
	if strings.Contains(strings.Join(args, " "), " test -e ") {
		return model.CommandResult{ExitCode: r.code, Stderr: "test: bad"}
	}

	return r.inner.Run(ctx, args)
}

func TestObserve_ARunningJobWhoseMarkerReadFailsIsUnknownAndNeverPending(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		code int
		want error
	}{
		"a test that exits with a status other than 0 or 1": {code: 2, want: link.ErrCommand},
		"a dropped ssh link": {code: 255, want: link.ErrDropped},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, f, _ := newClockedHost(t)
			id, job := submitted(t, h, f)
			job.Live = 5

			parsed, err := parseJobID(id)
			require.NoError(t, err)

			state, size, err := scheduler{link.New(slurmtest.Target, testExit{inner: f.SSH(), code: tc.code})}.Observe(context.Background(), parsed)

			require.ErrorIs(t, err, tc.want)
			assert.Equal(t, model.JobUnknown, state)
			assert.EqualValues(t, -1, size)
		})
	}
}

func TestUp_RefusesAHeadNodeWithoutFlock(t *testing.T) {
	t.Parallel()

	h, f, _, _ := newClusterHost(t, hosttest.Working, hostConfig())
	f.NoFlock = true

	err := h.Up(t.Context())

	require.ErrorIs(t, err, link.ErrNoFlock)
	assert.ErrorContains(t, err, "Install util-linux (flock)")
}
