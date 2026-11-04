package slurm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/hosttest"
	"github.com/tobocop2/obee/internal/host/jobwait"
	"github.com/tobocop2/obee/internal/host/link"
	"github.com/tobocop2/obee/internal/host/slurmtest"
	"github.com/tobocop2/obee/internal/model"
)

// fakeClock is the clock of a wait under test. A pause moves the clock; no test sleeps for real time.
type fakeClock struct {
	now    time.Time
	pauses int
	limit  int // when above zero, the pause after this many pauses fails, so a wait that never ends still ends the test
}

var errStepLimit = errors.New("the test step limit")

// clockPoll is the time between two polls on the fake clock.
const clockPoll = 10 * time.Second

func (c *fakeClock) read() time.Time { return c.now }

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	c.pauses++
	c.now = c.now.Add(d)

	if c.limit > 0 && c.pauses > c.limit {
		return errStepLimit
	}

	return ctx.Err()
}

// newClockedHost returns a host over the fake Slurm whose waits run on a fake clock. The host is up.
func newClockedHost(t *testing.T) (*Queue, *slurmtest.Cluster, *fakeClock) {
	t.Helper()

	return newClockedHostWith(t, 0)
}

// newClockedHostWith is newClockedHost for a project that sets host.stale_minutes. Each with function makes an option from the clock and the
// fake cluster; a later option replaces an earlier one.
func newClockedHostWith(t *testing.T, staleMinutes int, with ...func(*fakeClock, *slurmtest.Cluster) Option) (*Queue, *slurmtest.Cluster, *fakeClock) {
	t.Helper()

	f := slurmtest.New(hosttest.Working)
	f.Up = true
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	opts := []Option{WithClock(jobwait.Clock{Poll: clockPoll, Now: clock.read, Sleep: clock.sleep}), WithBank(noBank)}

	for _, option := range with {
		opts = append(opts, option(clock, f))
	}

	h := newTestQueue(f, model.HostConfig{IdleMinutes: 60, MaxRun: 24 * time.Hour, StaleMinutes: staleMinutes}, opts...)

	return h, f, clock
}

func submitted(t *testing.T, h *Queue, f *slurmtest.Cluster) (model.JobID, *slurmtest.Job) {
	t.Helper()

	id, err := h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})
	require.NoError(t, err)

	slurmID, _, _ := strings.Cut(string(id), "-")

	return id, f.Jobs[slurmID]
}

func TestWait_DoneReadsTheFinalStateAndEndsTheSlot(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, _ := submitted(t, h, f)

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Zero(t, f.WaitingSlots())
}

func TestWait_DiedEndsOnlyAfterTheEmptyReadRepeatsForTheGrace(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.Vanished = true
	start := clock.now

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobDied, state)
	assert.GreaterOrEqual(t, clock.now.Sub(start), time.Minute, "an empty read repeats for the grace before it reads as died")
	assert.Less(t, clock.now.Sub(start), time.Minute+clockPoll+time.Second)
	assert.Zero(t, f.WaitingSlots(), "the slot of a dead job ends")
}

func TestWait_ARecordThatAppearsInTheGraceGivesItsRealState(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.Blank = 3

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state, "a job that just finished is not read as died")
}

func TestWait_AStaleStopWaitsForTheJobToEndBeforeItBanks(t *testing.T) {
	t.Parallel()

	var (
		job             *slurmtest.Job
		lingeringAtBank int
	)

	h, f, _ := newClockedHostWith(t, 0, func(*fakeClock, *slurmtest.Cluster) Option {
		return WithBank(func(context.Context) error {
			lingeringAtBank = job.Lingers

			return nil
		})
	})

	var id model.JobID

	id, job = submitted(t, h, f)
	job.Live, job.LogSizes, job.Lingers = 100000, []int{1}, 3

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobStale, state)
	assert.True(t, job.Canceled)
	assert.Zero(t, lingeringAtBank, "the bank runs after the job left the queue")
	assert.Zero(t, f.WaitingSlots())
}

func TestWait_StaleEndsAfterTheLogDoesNotGrowForTheLimitAndStopsTheJob(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.Live, job.LogSizes = 100000, []int{10, 20, 30}
	start := clock.now

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobStale, state)
	assert.True(t, job.Canceled, "the wait stops a stale job")
	assert.GreaterOrEqual(t, clock.now.Sub(start), 20*time.Minute)
	assert.Less(t, clock.now.Sub(start), 21*time.Minute)
	assert.Zero(t, f.WaitingSlots())
}

func TestWait_StaleLimitComesFromTheProjectValue(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHostWith(t, 3)
	id, job := submitted(t, h, f)
	job.Live, job.LogSizes = 100000, []int{7}
	start := clock.now

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobStale, state)
	assert.Less(t, clock.now.Sub(start), 4*time.Minute)
}

func TestWait_AGrowingLogIsNeverStale(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.Live = 500

	for size := 1; size <= 500; size++ {
		job.LogSizes = append(job.LogSizes, size)
	}

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.False(t, job.Canceled)
}

func TestWait_ALinkThatDropsAndReturnsKeepsWaitingOnTheSameJob(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, job := submitted(t, h, f)
	f.Drops = 20

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Equal(t, 1, f.Sbatches, "a drop never starts the job a second time")
	assert.False(t, job.Canceled)
}

func TestWait_ADropAfterLongerThanTheLostLimitOfAnsweredReadsKeepsWaiting(t *testing.T) {
	t.Parallel()

	start, dropped := time.Unix(1_000_000, 0), false
	h, f, _ := newClockedHostWith(t, 0, func(clock *fakeClock, f *slurmtest.Cluster) Option {
		return WithClock(jobwait.Clock{Poll: clockPoll, Now: clock.read, Sleep: func(ctx context.Context, d time.Duration) error {
			if !dropped && clock.now.Sub(start) > 6*time.Minute {
				dropped, f.Drops = true, 1
			}

			return clock.sleep(ctx, d)
		}})
	})
	id, job := submitted(t, h, f)
	job.Live, job.LogSizes = 60, []int{1}

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.True(t, dropped, "the link dropped once after six minutes of answered reads")
	assert.Equal(t, model.JobSucceeded, state)
	assert.False(t, job.Canceled)
}

func TestWait_LostEndsAfterFiveMinutesWithoutAnAnsweredReadAndLeavesTheJobRunning(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	f.Drops = -1
	start := clock.now

	state, err := h.Wait(context.Background(), id)

	require.ErrorIs(t, err, host.ErrLost)
	require.ErrorContains(t, err, "job "+string(id))
	require.ErrorContains(t, err, "scancel", "the error says how to cancel the job")
	require.ErrorContains(t, err, "only after the cancel")
	assert.Equal(t, model.JobLost, state)
	assert.False(t, state.Terminal())
	assert.GreaterOrEqual(t, clock.now.Sub(start), 5*time.Minute)
	assert.Less(t, clock.now.Sub(start), 5*time.Minute+clockPoll+time.Second)
	assert.Equal(t, 1, f.Sbatches)
	assert.False(t, job.Canceled, "the job keeps running on the host")
}

func TestWait_ADropOnTheLogReadOrTheRecordReadIsOneFailedReadNotTheEndOfTheWait(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"stat", "sacct"} {
		h, f, _ := newClockedHost(t)
		id, job := submitted(t, h, f)
		job.Live, job.LogSizes, f.DropOn = 3, []int{1, 2, 3, 4}, command

		state, err := h.Wait(context.Background(), id)

		require.NoError(t, err, command)
		assert.Equal(t, model.JobSucceeded, state, command)
		assert.Equal(t, 1, f.Sbatches, command)
	}
}

func TestWait_AStaleJobThatCannotBeStoppedSaysItMayStillRun(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.Live, job.LogSizes, f.NoStop = 100000, []int{1}, true

	state, err := h.Wait(context.Background(), id)

	require.ErrorIs(t, err, link.ErrCommand)
	require.ErrorContains(t, err, "may still run")
	assert.Equal(t, model.JobStale, state)
	assert.False(t, job.Canceled)
}

func TestWait_AStaleJobThatIgnoresTheCancelEndsTheStopWaitWithAnErrorThatNamesTheJob(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.Live, job.LogSizes, job.Lingers = 100000, []int{1}, 1<<30
	clock.limit = 1000

	state, err := h.Wait(context.Background(), id)

	require.NotErrorIs(t, err, errStepLimit, "the stop wait has its own end")
	require.ErrorContains(t, err, "job "+string(id))
	require.ErrorContains(t, err, "did not take")
	require.ErrorContains(t, err, "squeue")
	require.ErrorContains(t, err, "scancel")
	assert.Equal(t, model.JobStale, state)
	assert.True(t, job.Canceled)
	assert.Equal(t, 1, f.Scancels, "one job is canceled once")
}

func TestWait_ALogReadThatAlwaysDropsDoesNotCountAsAnAnsweredRead(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.Live, job.LogSizes, f.DropAll = 100000, []int{1}, "stat"
	clock.limit = 1000
	start := clock.now

	state, err := h.Wait(context.Background(), id)

	require.ErrorIs(t, err, host.ErrLost)
	assert.Equal(t, model.JobLost, state)
	assert.GreaterOrEqual(t, clock.now.Sub(start), 5*time.Minute)
	assert.Less(t, clock.now.Sub(start), 5*time.Minute+clockPoll+time.Second)
	assert.False(t, job.Canceled, "a job whose log was never read is not stopped as stale")
}

func TestWait_ALinkThatReturnsAfterTheLimitStartsNoSecondJob(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, _ := submitted(t, h, f)
	f.Drops = 40

	state, err := h.Wait(context.Background(), id)
	require.ErrorIs(t, err, host.ErrLost)
	assert.Equal(t, model.JobLost, state)

	f.Drops = 0
	state, err = h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Equal(t, 1, f.Sbatches)
}

func TestWait_ANewHandleWaitsOnTheRecordedJobAndSubmitsNoSecondCopy(t *testing.T) {
	t.Parallel()

	first, f, clock := newClockedHost(t)
	id, _ := submitted(t, first, f)

	second := newTestQueue(f, first.config, WithClock(jobwait.Clock{Poll: clockPoll, Now: clock.read, Sleep: clock.sleep}), WithBank(noBank))

	state, err := second.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Equal(t, 1, f.Sbatches)
}
