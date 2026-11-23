package slurm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/jobwait"
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

// testExit answers the remote read that names the waiting marker with an exit status, and every other call as the fake cluster does.
type testExit struct {
	inner proc.Runner
	code  int
}

func (r testExit) Run(ctx context.Context, args []string) model.CommandResult {
	if strings.Contains(strings.Join(args, " "), stateFolder+"/"+waitingPrefix) {
		return model.CommandResult{ExitCode: r.code, Stderr: "test: bad"}
	}

	return r.inner.Run(ctx, args)
}

// The job has a marker in both cases, so a read that failed and still read the marker would say pending.
func TestObserve_ARunningJobWhoseMarkerReadFailsIsRunningWithNoSizeAndNeverPending(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		code int
		want error
	}{
		"a read that exits with a status other than 0": {code: 2},
		"a dropped ssh link":                           {code: 255, want: link.ErrDropped},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, f, _ := newClockedHost(t)
			id, job := submitted(t, h, f)
			job.Live, job.LogSizes = 5, []int{10}
			f.Files[".beehave/waiting-"+jobNumber(t, id)] = true

			parsed, err := parseJobID(id)
			require.NoError(t, err)

			state, size, err := scheduler{link.New(slurmtest.Target, testExit{inner: f.SSH(), code: tc.code})}.Observe(context.Background(), parsed)

			require.ErrorIs(t, err, tc.want, "only a dropped link is an error")
			assert.Equal(t, model.JobRunning, state)
			assert.EqualValues(t, -1, size)
		})
	}
}

func TestWait_ARunningJobWhoseMarkerReadFailsKeepsWaitingAndEndsAsTheJobEnds(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.Live = 3

	failing := New(link.New(slurmtest.Target, testExit{inner: f.SSH(), code: 2}), h.config, fakeSlot{f},
		WithClock(jobwait.Clock{Poll: clockPoll, Now: clock.read, Sleep: clock.sleep}), WithBank(noBank))

	state, err := failing.Wait(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state, "a read of the marker that exits 2 is no marker, and the Wait ends on the final state of the job")
	assert.Equal(t, 4, job.Polls, "the Wait reads squeue at each of the three running polls and once more for the end")
}

func TestObserve_ARunningJobMakesTwoRemoteExecutionsPerPoll(t *testing.T) {
	t.Parallel()

	for name, marker := range map[string]bool{"a job that runs": false, "a job that waits for a slot": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, f, _ := newClockedHost(t)
			id, job := submitted(t, h, f)
			job.Live, job.LogSizes = 5, []int{10}
			f.Files[".beehave/waiting-"+jobNumber(t, id)] = marker

			parsed, err := parseJobID(id)
			require.NoError(t, err)

			s := &spy{inner: f.SSH()}
			_, _, err = scheduler{link.New(slurmtest.Target, s)}.Observe(t.Context(), parsed)

			require.NoError(t, err)
			assert.Len(t, s.calls, 2, "squeue and one read of the marker and the log size: %v", s.calls)
		})
	}
}

// hereShell runs the remote command of an ssh call under a real sh in dir, as the login shell of the head node runs the words that ssh sends.
type hereShell struct{ dir string }

func (r hereShell) Run(ctx context.Context, args []string) model.CommandResult {
	cmd := exec.CommandContext(ctx, "sh", "-c", strings.Join(args[3:], " ")) //nolint:gosec // the test runs the words that the link sends to the head node
	cmd.Dir = r.dir
	out, err := cmd.Output()

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return model.CommandResult{Stdout: string(out), ExitCode: exit.ExitCode()}
	}

	return model.CommandResult{Stdout: string(out)}
}

func TestObserve_TheReadOfARunningJobUnderARealShellFindsTheMarkerAndTheLogSize(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".beehave"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".beehave", "waiting-7"), nil, 0o600))

	for _, number := range []string{"7", "8"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "slurm-"+number+".out"), []byte("12345"), 0o600))
	}

	want := int64(5)
	if exec.CommandContext(t.Context(), "stat", "-c", "%s", filepath.Join(dir, "slurm-8.out")).Run() != nil { //nolint:gosec // the path is a file of the test's own temp folder
		want = -1 // a stat with no -c, as on macOS, gives no size
	}

	s := scheduler{link.New("lane-host", hereShell{dir})}

	waiting, size, err := s.running(t.Context(), jobwait.Job{ID: "7", Nonce: "n"})
	require.NoError(t, err)
	assert.True(t, waiting, "the job with a marker waits")
	assert.Equal(t, want, size)

	waiting, size, err = s.running(t.Context(), jobwait.Job{ID: "8", Nonce: "n"})
	require.NoError(t, err)
	assert.False(t, waiting, "the job with no marker runs")
	assert.Equal(t, want, size)

	waiting, size, err = s.running(t.Context(), jobwait.Job{ID: "9", Nonce: "n"})
	require.NoError(t, err)
	assert.False(t, waiting)
	assert.EqualValues(t, -1, size, "a job with no log has no size")
}

func TestUp_RefusesAHeadNodeWithoutFlock(t *testing.T) {
	t.Parallel()

	h, f, _, _ := newClusterHost(t, hosttest.Working, hostConfig())
	f.NoFlock = true

	err := h.Up(t.Context())

	require.ErrorIs(t, err, link.ErrNoFlock)
	assert.ErrorContains(t, err, "Install util-linux (flock)")
}
