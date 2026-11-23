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
type diskHost struct {
	stubHost

	mu        sync.Mutex
	free      uint64
	readable  bool
	reads     int
	cancelErr error
	freed     uint64 // the free space after a cancel
	jobs      map[model.JobID]chan model.JobState
	canceled  []model.JobID
}

func newDiskHost(free uint64) *diskHost {
	return &diskHost{free: free, readable: true, freed: 60 * gb, jobs: map[model.JobID]chan model.JobState{}}
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

func (d *diskHost) Wait(_ context.Context, id model.JobID) (model.JobState, error) {
	d.mu.Lock()
	ended := d.jobs[id]
	d.mu.Unlock()

	return <-ended, nil
}

func (d *diskHost) Cancel(_ context.Context, id model.JobID) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.canceled = append(d.canceled, id)

	if d.cancelErr != nil {
		return d.cancelErr
	}

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

// waitAsync runs Wait of the guard for the job and returns the channel of its result.
func waitAsync(ctx context.Context, h host.Host, id model.JobID) <-chan waited {
	out := make(chan waited, 1)

	go func() {
		state, err := h.Wait(ctx, id)
		out <- waited{state, err}
	}()

	return out
}

// twoJobs submits two jobs through a guard on a host with enough space, then drops the free space under the floor.
func twoJobs(t *testing.T, d *diskHost) host.Host {
	t.Helper()

	guard := host.WithDiskWait(d, 50, 0, watchPoll, host.NewFleet())

	first, err := guard.Submit(t.Context(), model.JobSpec{Name: "a"})
	require.NoError(t, err)
	second, err := guard.Submit(t.Context(), model.JobSpec{Name: "b"})
	require.NoError(t, err)
	require.Equal(t, []model.JobID{"1", "2"}, []model.JobID{first, second})

	return guard
}

func TestDiskWatch_AHostUnderTheFloorStopsTheYoungestJobWithADiskReason(t *testing.T) {
	t.Parallel()

	d := newDiskHost(60 * gb)
	guard := twoJobs(t, d)
	d.setFree(10 * gb)

	older := waitAsync(t.Context(), guard, "1")
	younger := waitAsync(t.Context(), guard, "2")

	got := <-younger
	require.NoError(t, got.err)
	assert.Equal(t, model.JobDiskFull, got.state, "the youngest job ends with the disk reason, not as canceled or failed")
	assert.Equal(t, []model.JobID{"2"}, d.cancels(), "only the youngest job stops: the stop frees the space, so the older job keeps running")

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
	guard := twoJobs(t, d)
	ended := waitAsync(t.Context(), guard, "2")
	base := d.statusReads()

	require.Eventually(t, func() bool { return d.statusReads() >= base+3 }, 5*time.Second, time.Millisecond, "the guard reads the disk at each poll")
	d.finish("2")

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
		d := newDiskHost(60 * gb)
		guard := twoJobs(t, d)

		d.mu.Lock()
		unread(d)
		d.free = 0
		d.mu.Unlock()

		ended := waitAsync(t.Context(), guard, "2")
		base := d.statusReads()

		require.Eventually(t, func() bool { return d.statusReads() >= base+3 }, 5*time.Second, time.Millisecond, name)
		d.finish("2")

		got := <-ended
		require.NoError(t, got.err, name)
		assert.Equal(t, model.JobSucceeded, got.state, name)
		assert.Empty(t, d.cancels(), name)
	}
}

func TestDiskWatch_AStopThatFailsIsAnErrorAndTheJobKeepsItsState(t *testing.T) {
	t.Parallel()

	d := newDiskHost(60 * gb)
	d.cancelErr = errStop
	guard := twoJobs(t, d)
	d.setFree(10 * gb)

	ended := waitAsync(t.Context(), guard, "2")

	require.Eventually(t, func() bool { return len(d.cancels()) == 1 }, 5*time.Second, time.Millisecond)
	d.finish("2")

	got := <-ended
	require.ErrorIs(t, got.err, errStop)
	assert.Contains(t, got.err.Error(), "may still run")
	assert.Equal(t, model.JobSucceeded, got.state, "a job that was not stopped keeps the state it ended in")
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

func TestDiskWatch_AJobTheGuardDidNotStartIsNeverStopped(t *testing.T) {
	t.Parallel()

	d := newDiskHost(10 * gb)
	d.jobs["9"] = make(chan model.JobState, 1)

	guard := host.WithDiskWait(d, 50, 0, watchPoll, host.NewFleet())
	ended := waitAsync(t.Context(), guard, "9")

	time.Sleep(10 * watchPoll)
	d.finish("9")

	got := <-ended
	require.NoError(t, got.err)
	assert.Equal(t, model.JobSucceeded, got.state)
	assert.Zero(t, d.statusReads(), "a job outside the fleet is not the youngest, so its wait reads nothing")
	assert.Empty(t, d.cancels())
}
