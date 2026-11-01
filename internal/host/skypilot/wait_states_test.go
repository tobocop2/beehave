package skypilot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/hosttest"
	"github.com/tobocop2/obee/internal/model"
)

// fakeClock is the clock of a wait under test. A pause moves the clock; no test sleeps for real time.
type fakeClock struct {
	now    time.Time
	pauses int
	limit  int // when above zero, the pause after this many pauses fails, so a wait that never ends still ends the test
}

var errStepLimit = errors.New("the test step limit")

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
func newClockedHost(t *testing.T) (*Host, *fakeHost, *fakeClock) {
	t.Helper()

	f := newFakeHost(hosttest.Working)
	f.up = true
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}

	h, err := New(fakeCluster, model.HostConfig{Provider: model.HostSkyPilot, IdleMinutes: 60, MaxRun: 24 * time.Hour}, f.sky(), f.ssh())
	require.NoError(t, err)

	h.poll = 10 * time.Second
	h.now, h.sleep = clock.read, clock.sleep
	h.bank = func(context.Context) error { return nil }

	return h, f, clock
}

func submitted(t *testing.T, h *Host, f *fakeHost) (model.JobID, *fakeJob) {
	t.Helper()

	id, err := h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})
	require.NoError(t, err)

	handle, err := parseJobID(id)
	require.NoError(t, err)

	return id, f.jobs[handle.slurm]
}

func TestWait_DoneReadsTheFinalStateAndEndsTheSlot(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, _ := submitted(t, h, f)

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Zero(t, f.waitingSlots())
}

func TestWait_DiedEndsOnlyAfterTheEmptyReadRepeatsForTheGrace(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.vanished = true
	start := clock.now

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobDied, state)
	assert.GreaterOrEqual(t, clock.now.Sub(start), time.Minute, "an empty read repeats for the grace before it reads as died")
	assert.Less(t, clock.now.Sub(start), time.Minute+h.poll+time.Second)
	assert.Zero(t, f.waitingSlots(), "the slot of a dead job ends")
}

func TestWait_ARecordThatAppearsInTheGraceGivesItsRealState(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.blank = 3

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state, "a job that just finished is not read as died")
}

func TestWait_AStaleStopWaitsForTheJobToEndBeforeItBanks(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.live, job.logSizes, job.lingers = 100000, []int{1}, 3

	var lingeringAtBank int

	h.bank = func(context.Context) error {
		lingeringAtBank = job.lingers

		return nil
	}

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobStale, state)
	assert.True(t, job.canceled)
	assert.Zero(t, lingeringAtBank, "the bank runs after the job left the queue")
	assert.Zero(t, f.waitingSlots())
}

func TestWait_StaleEndsAfterTheLogDoesNotGrowForTheLimitAndStopsTheJob(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.live, job.logSizes = 100000, []int{10, 20, 30}
	start := clock.now

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobStale, state)
	assert.True(t, job.canceled, "the wait stops a stale job")
	assert.GreaterOrEqual(t, clock.now.Sub(start), 20*time.Minute)
	assert.Less(t, clock.now.Sub(start), 21*time.Minute)
	assert.Zero(t, f.waitingSlots())
}

func TestWait_StaleLimitComesFromTheProjectValue(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	h.config.StaleMinutes = 3
	id, job := submitted(t, h, f)
	job.live, job.logSizes = 100000, []int{7}
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
	job.live = 500

	for size := 1; size <= 500; size++ {
		job.logSizes = append(job.logSizes, size)
	}

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.False(t, job.canceled)
}

func TestWait_ALinkThatDropsAndReturnsKeepsWaitingOnTheSameJob(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, job := submitted(t, h, f)
	f.drops = 20

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Equal(t, 1, f.sbatches, "a drop never starts the job a second time")
	assert.False(t, job.canceled)
}

func TestWait_ADropAfterLongerThanTheLostLimitOfAnsweredReadsKeepsWaiting(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.live, job.logSizes = 60, []int{1}
	start, dropped := clock.now, false
	h.sleep = func(ctx context.Context, d time.Duration) error {
		if !dropped && clock.now.Sub(start) > 6*time.Minute {
			dropped, f.drops = true, 1
		}

		return clock.sleep(ctx, d)
	}

	state, err := h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.True(t, dropped, "the link dropped once after six minutes of answered reads")
	assert.Equal(t, model.JobSucceeded, state)
	assert.False(t, job.canceled)
}

func TestWait_LostEndsAfterFiveMinutesWithoutAnAnsweredReadAndLeavesTheJobRunning(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	f.drops = -1
	start := clock.now

	state, err := h.Wait(context.Background(), id)

	require.ErrorIs(t, err, host.ErrLost)
	require.ErrorContains(t, err, "job "+string(id))
	require.ErrorContains(t, err, "scancel", "the error says how to cancel the job")
	require.ErrorContains(t, err, "only after the cancel")
	assert.Equal(t, model.JobLost, state)
	assert.False(t, state.Terminal())
	assert.GreaterOrEqual(t, clock.now.Sub(start), 5*time.Minute)
	assert.Less(t, clock.now.Sub(start), 5*time.Minute+h.poll+time.Second)
	assert.Equal(t, 1, f.sbatches)
	assert.False(t, job.canceled, "the job keeps running on the host")
}

func TestWait_ADropOnTheLogReadOrTheRecordReadIsOneFailedReadNotTheEndOfTheWait(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"stat", "sacct"} {
		h, f, _ := newClockedHost(t)
		id, job := submitted(t, h, f)
		job.live, job.logSizes, f.dropOn = 3, []int{1, 2, 3, 4}, command

		state, err := h.Wait(context.Background(), id)

		require.NoError(t, err, command)
		assert.Equal(t, model.JobSucceeded, state, command)
		assert.Equal(t, 1, f.sbatches, command)
	}
}

func TestWait_AStaleJobThatCannotBeStoppedSaysItMayStillRun(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.live, job.logSizes, f.noStop = 100000, []int{1}, true

	state, err := h.Wait(context.Background(), id)

	require.ErrorIs(t, err, errCommand)
	require.ErrorContains(t, err, "may still run")
	assert.Equal(t, model.JobStale, state)
	assert.False(t, job.canceled)
}

func TestWait_AStaleJobThatIgnoresTheCancelEndsTheStopWaitWithAnErrorThatNamesTheJob(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.live, job.logSizes, job.lingers = 100000, []int{1}, 1<<30
	clock.limit = 1000

	state, err := h.Wait(context.Background(), id)

	require.NotErrorIs(t, err, errStepLimit, "the stop wait has its own end")
	require.ErrorContains(t, err, "job "+string(id))
	require.ErrorContains(t, err, "did not take")
	require.ErrorContains(t, err, "squeue")
	require.ErrorContains(t, err, "scancel")
	assert.Equal(t, model.JobStale, state)
	assert.True(t, job.canceled)
	assert.Equal(t, 1, f.scancels, "one job is canceled once")
}

func TestWait_ALogReadThatAlwaysDropsDoesNotCountAsAnAnsweredRead(t *testing.T) {
	t.Parallel()

	h, f, clock := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.live, job.logSizes, f.dropAll = 100000, []int{1}, "stat"
	clock.limit = 1000
	start := clock.now

	state, err := h.Wait(context.Background(), id)

	require.ErrorIs(t, err, host.ErrLost)
	assert.Equal(t, model.JobLost, state)
	assert.GreaterOrEqual(t, clock.now.Sub(start), 5*time.Minute)
	assert.Less(t, clock.now.Sub(start), 5*time.Minute+h.poll+time.Second)
	assert.False(t, job.canceled, "a job whose log was never read is not stopped as stale")
}

func TestWait_ALinkThatReturnsAfterTheLimitStartsNoSecondJob(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, _ := submitted(t, h, f)
	f.drops = 40

	state, err := h.Wait(context.Background(), id)
	require.ErrorIs(t, err, host.ErrLost)
	assert.Equal(t, model.JobLost, state)

	f.drops = 0
	state, err = h.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Equal(t, 1, f.sbatches)
}

func TestWait_ANewHandleWaitsOnTheRecordedJobAndSubmitsNoSecondCopy(t *testing.T) {
	t.Parallel()

	first, f, clock := newClockedHost(t)
	id, _ := submitted(t, first, f)

	second, err := New(fakeCluster, first.config, f.sky(), f.ssh())
	require.NoError(t, err)

	second.now, second.sleep, second.bank = clock.read, clock.sleep, func(context.Context) error { return nil }

	state, err := second.Wait(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Equal(t, 1, f.sbatches)
}
