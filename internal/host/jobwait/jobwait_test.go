package jobwait_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host"
	"github.com/tobocop2/beehest/internal/host/jobwait"
	"github.com/tobocop2/beehest/internal/host/link"
	"github.com/tobocop2/beehest/internal/model"
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
	return limitedEngine(s, c, bank, sl, stale, jobwait.Limits{})
}

func limitedEngine(s jobwait.Scheduler, c *clock, bank func(context.Context) error, sl slot, stale time.Duration, limits jobwait.Limits) *jobwait.Engine {
	return jobwait.New(s, sl.Release, jobwait.Clock{Poll: 10 * time.Second, Now: c.read, Sleep: c.sleep}, func(ctx context.Context, _ string) error { return bank(ctx) }, stale, limits)
}

func TestJob_StringHoldsTheFolderAndSplitFolderReadsItBack(t *testing.T) {
	t.Parallel()

	withFolder := jobwait.Job{ID: "7", Nonce: "n", Folder: "/home/ci/beehest-gate-a-1"}

	assert.Equal(t, "7-n", withFolder.Key())
	assert.NotEqual(t, withFolder.Key(), withFolder.String())

	key, folder, err := jobwait.SplitFolder(withFolder.String())
	require.NoError(t, err)
	assert.Equal(t, "7-n", key)
	assert.Equal(t, "/home/ci/beehest-gate-a-1", folder)

	key, folder, err = jobwait.SplitFolder("7-n")
	require.NoError(t, err)
	assert.Equal(t, "7-n", key)
	assert.Empty(t, folder, "an id with no folder gives none")
}

func TestSplitFolder_RefusesAFolderThatIsNotAnAbsolutePath(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"7-n~!!!", "7-n~" + base64.RawURLEncoding.EncodeToString([]byte("relative/dir")), "7-n~" + base64.RawURLEncoding.EncodeToString([]byte("/a\nb"))} {
		_, _, err := jobwait.SplitFolder(text)
		require.ErrorIs(t, err, jobwait.ErrBadFolder, text)
	}
}

func TestCheckFolder_AcceptsNoFolderAndAnAbsolutePathOnly(t *testing.T) {
	t.Parallel()

	require.NoError(t, jobwait.CheckFolder(""))
	require.NoError(t, jobwait.CheckFolder("/home/ci/x"))
	require.ErrorIs(t, jobwait.CheckFolder("x/y"), jobwait.ErrBadFolder)
	require.ErrorIs(t, jobwait.CheckFolder("/x\x00"), jobwait.ErrBadFolder)
}

func TestScopedBank_AJobWithAFolderBanksOnlyThatFolderAndOneWithNoneBanksTheRoot(t *testing.T) {
	t.Parallel()

	var calls []string

	bank := jobwait.ScopedBank(
		func(context.Context) error { calls = append(calls, "root"); return nil },
		func(_ context.Context, folder string) error { calls = append(calls, folder); return nil },
	)

	require.NoError(t, bank(t.Context(), "/home/ci/a"))
	require.NoError(t, bank(t.Context(), ""))
	assert.Equal(t, []string{"/home/ci/a", "root"}, calls)
}

func TestWait_AJobWithAFolderBanksThatFolderWhenItEnds(t *testing.T) {
	t.Parallel()

	var banked []string

	c := newClock()
	e := jobwait.New(&scripted{reads: []reading{{state: model.JobSucceeded}}}, slot{}.Release, jobwait.Clock{Poll: 10 * time.Second, Now: c.read, Sleep: c.sleep},
		func(_ context.Context, folder string) error { banked = append(banked, folder); return nil }, 0, jobwait.Limits{})

	_, err := e.Wait(t.Context(), jobwait.Job{ID: "7", Nonce: "n", Folder: "/home/ci/a"})

	require.NoError(t, err)
	assert.Equal(t, []string{"/home/ci/a"}, banked)
}

func noBank(context.Context) error { return nil }

func newClock() *clock { return &clock{now: time.Unix(1_000_000, 0)} }

func TestJob_StringJoinsTheIDAndTheNonce(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "7-n", job.String())
}

func TestJob_StringOfAJobWithNoNonceIsItsID(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "beehest-x", jobwait.Job{ID: "beehest-x"}.String())
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

func TestWait_ADroppedLinkEndsAsLostAfterTheLostAfterOfTheLimits(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobUnknown, err: link.ErrDropped}}}
	c := newClock()
	start := c.now

	state, err := limitedEngine(s, c, noBank, slot{}, 0, jobwait.Limits{LostAfter: time.Minute}).Wait(t.Context(), job)

	require.ErrorIs(t, err, host.ErrLost)
	require.ErrorContains(t, err, "answered for 1m0s")
	assert.Equal(t, model.JobLost, state)
	assert.GreaterOrEqual(t, c.now.Sub(start), time.Minute)
	assert.Less(t, c.now.Sub(start), 2*time.Minute)
}

func TestCancel_AJobThatIsNotFinalAfterTheStopLimitOfTheLimitsIsAnError(t *testing.T) {
	t.Parallel()

	s := &scripted{reads: []reading{{state: model.JobPending}}}
	c := newClock()
	start := c.now

	err := limitedEngine(s, c, noBank, slot{}, 0, jobwait.Limits{StopLimit: 2 * time.Minute}).Cancel(t.Context(), job)

	require.ErrorContains(t, err, "was not final 2m0s after the cancel")
	assert.GreaterOrEqual(t, c.now.Sub(start), 2*time.Minute)
	assert.Less(t, c.now.Sub(start), 2*time.Minute+10*time.Second)
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
