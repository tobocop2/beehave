package host_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
)

const watchPoll = 2 * time.Millisecond

var errStop = errors.New("the stop failed")

// diskHost is a host whose jobs run until the test ends them or the host cancels them. Its free space is set by the test.
// A cancel takes a moment to free the disk, as a job that ends takes a moment to leave its files.
type diskHost struct {
	stubHost

	mu          sync.Mutex
	free        uint64
	readable    bool
	reads       int
	cancelErr   error
	cancelFails int // the number of cancels that return cancelErr before one succeeds
	// cancelAlways makes every cancel return cancelErr.
	cancelAlways bool
	freed        uint64
	jobs         map[model.JobID]chan model.JobState
	canceled     []model.JobID
	entered      chan model.JobID
}

func newDiskHost(free uint64) *diskHost {
	return &diskHost{free: free, readable: true, freed: 60 * gb, jobs: map[model.JobID]chan model.JobState{}, entered: make(chan model.JobID, 16)}
}

func (d *diskHost) Status(context.Context) (model.HostStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.reads++

	if d.statusErr != nil {
		return model.HostStatus{}, d.statusErr
	}

	return model.HostStatus{State: model.HostUp, Disk: model.DiskReading{FreeBytes: d.free, Readable: d.readable}}, nil
}

func (d *diskHost) Submit(context.Context, model.JobSpec) (model.JobID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	id := model.JobID(strconv.Itoa(len(d.jobs) + 1))
	d.jobs[id] = make(chan model.JobState, 1)

	return id, nil
}

// addJob makes a job that the host runs and that no guard submitted, as a job that an earlier beehave started.
func (d *diskHost) addJob(ids ...model.JobID) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, id := range ids {
		d.jobs[id] = make(chan model.JobState, 1)
	}
}

// Wait says the job is entered, and then waits for its end. The guard has put the job in its fleet before this call.
func (d *diskHost) Wait(_ context.Context, id model.JobID) (model.JobState, error) {
	d.mu.Lock()
	ended := d.jobs[id]
	d.mu.Unlock()

	d.entered <- id

	return <-ended, nil
}

func (d *diskHost) Cancel(_ context.Context, id model.JobID) error {
	d.mu.Lock()
	d.canceled = append(d.canceled, id)

	if d.cancelAlways || d.cancelFails > 0 {
		d.cancelFails = max(d.cancelFails-1, 0)
		d.mu.Unlock()

		return d.cancelErr
	}

	d.mu.Unlock()

	time.Sleep(30 * time.Millisecond)

	d.mu.Lock()
	defer d.mu.Unlock()

	d.free = d.freed
	d.jobs[id] <- model.JobCanceled

	return nil
}

func (d *diskHost) setFree(free uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.free = free
}

func (d *diskHost) finish(id model.JobID) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.jobs[id] <- model.JobSucceeded
}

func (d *diskHost) cancels() []model.JobID {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]model.JobID(nil), d.canceled...)
}

func (d *diskHost) statusReads() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.reads
}

type waited struct {
	state model.JobState
	err   error
}

// startWait runs Wait of the guard for the job, and returns when the guard has put the job in its fleet and the host has begun to wait.
func startWait(t *testing.T, d *diskHost, guard host.Host, id model.JobID) <-chan waited {
	t.Helper()

	out := make(chan waited, 1)

	go func() {
		state, err := guard.Wait(t.Context(), id)
		out <- waited{state, err}
	}()

	select {
	case got := <-d.entered:
		require.Equal(t, id, got)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the wait did not begin", "job %s", id)
	}

	return out
}

func newGuard(d *diskHost) host.Host {
	return host.WithDiskWait(d, 50, 0, watchPoll, host.NewFleet())
}

func TestDiskWatch_AHostUnderTheFloorStopsTheYoungestJobWithADiskReason(t *testing.T) {
	t.Parallel()

	d := newDiskHost(60 * gb)
	d.addJob("1", "2")

	guard := newGuard(d)
	older := startWait(t, d, guard, "1")
	younger := startWait(t, d, guard, "2")

	d.setFree(10 * gb)

	got := <-younger
	require.NoError(t, got.err)
	assert.Equal(t, model.JobDiskFull, got.state, "the youngest job ends with the disk reason, not as canceled or failed")
	assert.Equal(t, []model.JobID{"2"}, d.cancels(), "only the youngest job stops: the stop frees the space, and the older job keeps running")

	d.finish("1")

	got = <-older
	require.NoError(t, got.err)
	assert.Equal(t, model.JobSucceeded, got.state)
}

func TestDiskWatch_AStopForTheDiskIsNotAFailedJob(t *testing.T) {
	t.Parallel()

	assert.True(t, model.JobDiskFull.Terminal())
	assert.NotEqual(t, model.JobFailed, model.JobDiskFull)
}

func TestDiskWatch_AHostWithEnoughSpaceStopsNoJob(t *testing.T) {
	t.Parallel()

	d := newDiskHost(60 * gb)
	d.addJob("1")

	ended := startWait(t, d, newGuard(d), "1")
	base := d.statusReads()

	require.Eventually(t, func() bool { return d.statusReads() >= base+3 }, 5*time.Second, time.Millisecond, "the guard reads the disk at each poll")
	d.finish("1")

	got := <-ended
	require.NoError(t, got.err)
	assert.Equal(t, model.JobSucceeded, got.state)
	assert.Empty(t, d.cancels())
}

func TestDiskWatch_AnUnreadableDiskStopsNoJob(t *testing.T) {
	t.Parallel()

	for name, unread := range map[string]func(*diskHost){
		"the host says the reading is not readable": func(d *diskHost) { d.readable = false },
		"the status read fails":                     func(d *diskHost) { d.statusErr = errDown },
	} {
		d := newDiskHost(0)
		d.addJob("1")
		unread(d)

		ended := startWait(t, d, newGuard(d), "1")
		base := d.statusReads()

		require.Eventually(t, func() bool { return d.statusReads() >= base+3 }, 5*time.Second, time.Millisecond, name)
		d.finish("1")

		got := <-ended
		require.NoError(t, got.err, name)
		assert.Equal(t, model.JobSucceeded, got.state, name)
		assert.Empty(t, d.cancels(), name)
	}
}

func TestDiskWatch_AStopThatKeepsFailingIsAnErrorAndTheJobKeepsItsState(t *testing.T) {
	t.Parallel()

	d := newDiskHost(10 * gb)
	d.addJob("1")
	d.cancelErr, d.cancelAlways = errStop, true

	ended := startWait(t, d, newGuard(d), "1")

	require.Eventually(t, func() bool { return len(d.cancels()) >= 2 }, 5*time.Second, time.Millisecond, "a failed stop is tried again at the next poll")
	d.finish("1")

	got := <-ended
	require.ErrorIs(t, got.err, errStop)
	assert.Contains(t, got.err.Error(), "may still run")
	assert.Equal(t, model.JobSucceeded, got.state, "a job that was not stopped keeps the state it ended in")
}

func TestDiskWatch_AStopThatFailsOnceIsTriedAgainAndTheJobEndsForTheDisk(t *testing.T) {
	t.Parallel()

	d := newDiskHost(10 * gb)
	d.addJob("1")
	d.cancelErr, d.cancelFails = errStop, 1

	got := <-startWait(t, d, newGuard(d), "1")

	require.NoError(t, got.err, "the second stop took, so the wait has no error")
	assert.Equal(t, model.JobDiskFull, got.state)
	assert.Equal(t, []model.JobID{"1", "1"}, d.cancels())
}

func TestDiskWatch_AGuardWithNoPollDoesNotReadTheDiskWhileAJobRuns(t *testing.T) {
	t.Parallel()

	d := newDiskHost(60 * gb)
	guard := host.WithDiskFloor(d, 50)

	id, err := guard.Submit(t.Context(), model.JobSpec{Name: "a"})
	require.NoError(t, err)

	d.setFree(10 * gb)

	base := d.statusReads()
	d.finish(id)

	state, err := guard.Wait(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Equal(t, base, d.statusReads())
	assert.Empty(t, d.cancels())
}

func TestDiskWatch_AJobThatABeehaveBeforeStartedIsStoppedLikeAnyOtherJob(t *testing.T) {
	t.Parallel()

	d := newDiskHost(10 * gb)
	d.addJob("9") // the guard never submitted it: a restart waits for it again

	got := <-startWait(t, d, newGuard(d), "9")

	require.NoError(t, got.err)
	assert.Equal(t, model.JobDiskFull, got.state)
	assert.Equal(t, []model.JobID{"9"}, d.cancels())
}

func TestDiskWatch_AJobThatIsSubmittedAndNeverWaitedDoesNotBlockTheOthers(t *testing.T) {
	t.Parallel()

	d := newDiskHost(60 * gb)
	guard := newGuard(d)

	first, err := guard.Submit(t.Context(), model.JobSpec{Name: "a"})
	require.NoError(t, err)

	_, err = guard.Submit(t.Context(), model.JobSpec{Name: "b"}) // the second job: nobody waits for it
	require.NoError(t, err)

	ended := startWait(t, d, guard, first)

	d.setFree(10 * gb)

	got := <-ended
	require.NoError(t, got.err)
	assert.Equal(t, model.JobDiskFull, got.state, "the job that is waited for is the youngest of the fleet")
	assert.Equal(t, []model.JobID{first}, d.cancels())
}

func TestDiskWatch_TwoWaitsThatEndInEitherOrderLeaveTheLastOneAsTheYoungest(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ first, last model.JobID }{
		"the older job ends first":   {first: "1", last: "2"},
		"the younger job ends first": {first: "2", last: "1"},
	} {
		d := newDiskHost(60 * gb)
		d.addJob("1", "2")

		guard := newGuard(d)
		results := map[model.JobID]<-chan waited{"1": startWait(t, d, guard, "1"), "2": startWait(t, d, guard, "2")}

		d.finish(tc.first)
		require.Equal(t, model.JobSucceeded, (<-results[tc.first]).state, name)
		d.setFree(10 * gb)

		got := <-results[tc.last]
		require.NoError(t, got.err, name)
		assert.Equal(t, model.JobDiskFull, got.state, name)
		assert.Equal(t, []model.JobID{tc.last}, d.cancels(), name)
	}
}
