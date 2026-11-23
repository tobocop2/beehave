package slurm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// startWrapped runs the --wrap text of a submitted job under a real shell in dir, as Slurm does in the login folder, with the job number in the environment.
func startWrapped(t *testing.T, dir, number, text string) *exec.Cmd {
	t.Helper()

	// The job runs to its end after the test ends its context, so it takes a context that the end of the test does not cancel.
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), "sh", "-c", "SLURM_JOB_ID="+number+"; export SLURM_JOB_ID; "+text) //nolint:gosec // the test runs the text that Submit sent to sbatch, under a real shell
	cmd.Dir = dir
	require.NoError(t, cmd.Start())

	return cmd
}

// The wrapper and the observe are written apart: one writes the marker with a shell, the other reads it with test. The wrapper runs here for real, and the
// file it writes goes into the fake cluster under its own name, so Wait reads pending only when the two paths agree.
func TestWait_TheMarkerThatTheWrapperWritesIsTheOneThatTheObserveReads(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock is not on PATH")
	}

	h, f, _ := newClockedHost(t)
	dir := t.TempDir()
	slot := model.Slot{Class: "mutation", Count: 1}

	held, release := filepath.Join(dir, "held"), filepath.Join(dir, "release")
	first, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "touch " + held + "; until test -e " + release + "; do sleep 0.05; done", Slot: slot})
	require.NoError(t, err)

	holder := startWrapped(t, dir, jobNumber(t, first), f.Jobs[jobNumber(t, first)].Command)
	t.Cleanup(func() {
		require.NoError(t, os.WriteFile(release, nil, 0o600))
		require.NoError(t, holder.Wait())
	})
	require.Eventually(t, func() bool { _, statErr := os.Stat(held); return statErr == nil }, 10*time.Second, 20*time.Millisecond, "the first job holds the only slot")

	second, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "true", Slot: slot})
	require.NoError(t, err)

	number := jobNumber(t, second)
	waiter := startWrapped(t, dir, number, f.Jobs[number].Command)
	t.Cleanup(func() {
		var killed *exec.ExitError

		require.NoError(t, waiter.Process.Kill())
		require.ErrorAs(t, waiter.Wait(), &killed)
	})

	require.Eventually(t, func() bool {
		names, globErr := filepath.Glob(filepath.Join(dir, ".beehave", "waiting-*"))

		return globErr == nil && len(names) == 1
	}, 10*time.Second, 20*time.Millisecond, "the waiting job writes one marker")

	written, err := filepath.Glob(filepath.Join(dir, ".beehave", "waiting-*"))
	require.NoError(t, err)

	rel, err := filepath.Rel(dir, written[0])
	require.NoError(t, err)

	job := f.Jobs[number]
	job.Live, job.LogSizes = 300, []int{10}
	f.Files[rel] = true

	state, err := h.Wait(t.Context(), second)

	require.NoError(t, err)
	assert.Equal(t, ".beehave/waiting-"+number, rel, "the wrapper names the marker by the job number")
	assert.Equal(t, model.JobSucceeded, state, "the observe finds the marker of the wrapper and keeps the job pending, so it is not stale")
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
