package jobwait_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/jobwait"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
)

var (
	errRead    = errors.New("the read failed")
	errStop    = errors.New("the stop failed")
	errBank    = errors.New("the bank failed")
	errRelease = errors.New("the release failed")
	errPause   = errors.New("the pause failed")
	errHang    = errors.New("the wait did not end")
)

// reading is one answer of the scripted scheduler. The last reading repeats.
type reading struct {
	state model.JobState
	size  int64
	err   error // the error of the state read
	logEr error // the error of the log read
}

// scripted is a Scheduler that answers from a list and records the calls.
type scripted struct {
	reads   []reading
	at      int
	stops   int
	stopErr error
	// onStop runs when Stop is called, so a test can turn the job final after the cancel.
	onStop func(*scripted)
}

func (s *scripted) current() reading { return s.reads[min(s.at, len(s.reads)-1)] }

func (s *scripted) Observe(context.Context, jobwait.Job) (model.JobState, int64, error) {
	r := s.current()
	s.at++

	if r.err != nil || r.state != model.JobRunning {
		return r.state, -1, r.err
	}

	return r.state, r.size, r.logEr
}

func (s *scripted) Stop(context.Context, jobwait.Job) error {
	s.stops++
	if s.onStop != nil {
		s.onStop(s)
	}

	return s.stopErr
}

func (*scripted) LostAdvice(jobwait.Job) string  { return "lost-advice" }
func (*scripted) StuckAdvice(jobwait.Job) string { return "stuck-advice" }

type slot struct{ err error }

func (s slot) Release(context.Context, jobwait.Job) error { return s.err }

const stepLimit = 2000 // a wait that needs more pauses than this is a hang

type clock struct {
	now   time.Time
	fails int // the pause that fails; zero means none
	count int
}

func (c *clock) read() time.Time { return c.now }

func (c *clock) sleep(_ context.Context, d time.Duration) error {
	c.count++
	c.now = c.now.Add(d)

	if c.count > stepLimit {
		return errHang
	}

	if c.fails > 0 && c.count >= c.fails {
		return errPause
	}

	return nil
}

var job = jobwait.Job{ID: "7", Nonce: "n"}

func engine(s jobwait.Scheduler, c *clock, bank func(context.Context) error, sl slot, stale time.Duration) *jobwait.Engine {
	return jobwait.New(s, sl.Release, jobwait.Clock{Poll: 10 * time.Second, Now: c.read, Sleep: c.sleep}, bank, stale)
}

func noBank(context.Context) error { return nil }

func newClock() *clock { return &clock{now: time.Unix(1_000_000, 0)} }

func TestJob_StringJoinsTheIDAndTheNonce(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "7-n", job.String())
}

func TestJob_StringOfAJobWithNoNonceIsItsID(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "beehave-x", jobwait.Job{ID: "beehave-x"}.String())
}

func TestWait_FinalStateBanksAndReleases(t *testing.T) {
	t.Parallel()

	banked := 0
	s := &scripted{reads: []reading{{state: model.JobSucceeded}}}
	e := engine(s, newClock(), func(context.Context) error { banked++; return nil }, slot{}, 0)

	state, err := e.Wait(t.Context(), job)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Equal(t, 1, banked)
}

func TestWait_ABankErrorIsReturnedWithTheStateAndNoReleaseRuns(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobFailed}}}
	e := engine(s, newClock(), func(context.Context) error { return errBank }, slot{err: errRelease}, 0)

	state, err := e.Wait(t.Context(), job)

	require.ErrorIs(t, err, errBank)
	require.NotErrorIs(t, err, errRelease)
	assert.Equal(t, model.JobFailed, state)
}

func TestWait_NoRepositoriesIsNothingToBank(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobSucceeded}}}
	e := engine(s, newClock(), func(context.Context) error { return host.ErrNoRepos }, slot{}, 0)

	_, err := e.Wait(t.Context(), job)

	require.NoError(t, err)
}

func TestWait_AReleaseErrorIsReturnedWithTheState(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobSucceeded}}}

	state, err := engine(s, newClock(), noBank, slot{err: errRelease}, 0).Wait(t.Context(), job)

	require.ErrorIs(t, err, errRelease)
	assert.Equal(t, model.JobSucceeded, state)
}

func TestWait_AnUnknownReadEndsTheWait(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobUnknown, err: errRead}}}

	state, err := engine(s, newClock(), noBank, slot{}, 0).Wait(t.Context(), job)

	require.ErrorIs(t, err, errRead)
	assert.Equal(t, model.JobUnknown, state)
}

func TestWait_APauseErrorEndsTheWait(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobPending}}}
	c := newClock()
	c.fails = 2

	state, err := engine(s, c, noBank, slot{}, 0).Wait(t.Context(), job)

	require.ErrorIs(t, err, errPause)
	assert.Equal(t, model.JobUnknown, state)
}

func TestWait_ADroppedLinkEndsAsLostAfterFiveMinutes(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobUnknown, err: link.ErrDropped}}}
	c := newClock()
	start := c.now

	state, err := engine(s, c, noBank, slot{}, 0).Wait(t.Context(), job)

	require.ErrorIs(t, err, host.ErrLost)
	require.ErrorContains(t, err, "lost-advice")
	assert.Equal(t, model.JobLost, state)
	assert.GreaterOrEqual(t, c.now.Sub(start), 5*time.Minute)
}

func TestWait_AnEmptyRecordIsPendingForTheGraceThenDied(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobDied}}}
	c := newClock()
	start := c.now

	state, err := engine(s, c, noBank, slot{}, 0).Wait(t.Context(), job)

	require.NoError(t, err)
	assert.Equal(t, model.JobDied, state)
	assert.GreaterOrEqual(t, c.now.Sub(start), time.Minute)
}

func TestWait_ARecordInTheGraceGivesItsRealState(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobDied}, {state: model.JobSucceeded}}}

	state, err := engine(s, newClock(), noBank, slot{}, 0).Wait(t.Context(), job)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
}

func TestWait_ALogThatDropsEveryReadEndsAsLost(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobRunning, logEr: link.ErrDropped}}}

	state, err := engine(s, newClock(), noBank, slot{}, 0).Wait(t.Context(), job)

	require.ErrorIs(t, err, host.ErrLost)
	assert.Equal(t, model.JobLost, state)
}

func TestWait_ALogThatKeepsGrowingIsNotStale(t *testing.T) {
	t.Parallel()

	reads := []reading{}
	for size := int64(1); size <= 12; size++ {
		reads = append(reads, reading{state: model.JobRunning, size: size})
	}

	reads = append(reads, reading{state: model.JobSucceeded})
	s := &scripted{reads: reads}

	state, err := engine(s, newClock(), noBank, slot{}, time.Minute).Wait(t.Context(), job)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Zero(t, s.stops)
}

func TestWait_AStillLogStopsTheJobAndEndsAsStale(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobRunning, size: 1}}}
	s.onStop = func(s *scripted) { s.reads, s.at = []reading{{state: model.JobCanceled}}, 0 }

	state, err := engine(s, newClock(), noBank, slot{}, time.Minute).Wait(t.Context(), job)

	require.NoError(t, err)
	assert.Equal(t, model.JobStale, state)
	assert.Equal(t, 1, s.stops)
}

func TestWait_AStaleJobWhoseStopFailsStillEndsAsStaleWithTheError(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobRunning, size: -1}}, stopErr: errStop}
	c := newClock()
	start := c.now

	state, err := engine(s, c, noBank, slot{}, 0).Wait(t.Context(), job)

	require.ErrorIs(t, err, errStop)
	assert.Equal(t, model.JobStale, state)
	assert.GreaterOrEqual(t, c.now.Sub(start), 20*time.Minute, "a log size of -1 is no growth, so the default stale time runs out")
}

func TestCancel_AJobThatIsNotFinalAfterTheLimitIsAnErrorWithTheAdvice(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobPending}}}
	c := newClock()
	start := c.now

	err := engine(s, c, noBank, slot{}, 0).Cancel(t.Context(), job)

	require.ErrorContains(t, err, "was not final")
	require.ErrorContains(t, err, "stuck-advice")
	assert.GreaterOrEqual(t, c.now.Sub(start), 5*time.Minute)
	assert.Less(t, c.now.Sub(start), 5*time.Minute+10*time.Second)
}

func TestCancel_ReturnsTheStopError(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobPending}}, stopErr: errStop}

	err := engine(s, newClock(), noBank, slot{}, 0).Cancel(t.Context(), job)

	require.ErrorIs(t, err, errStop)
}

func TestCancel_WaitsForTheFinalStateThenBanks(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobRunning, size: 1}, {state: model.JobCanceled}}}

	require.NoError(t, engine(s, newClock(), noBank, slot{}, 0).Cancel(t.Context(), job))
	assert.Equal(t, 1, s.stops)
}
