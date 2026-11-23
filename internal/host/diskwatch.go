package host

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tobocop2/beehave/internal/model"
)

// Fleet is the jobs that one build host runs through its disk guard, in the order they started. Every guard of one host shares one Fleet.
type Fleet struct {
	mu      sync.Mutex
	count   uint64
	running map[model.JobID]uint64
	stopped map[model.JobID]bool
}

// NewFleet returns a Fleet with no job.
func NewFleet() *Fleet {
	return &Fleet{running: map[model.JobID]uint64{}, stopped: map[model.JobID]bool{}}
}

func (f *Fleet) add(id model.JobID) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.count++
	f.running[id] = f.count
}

func (f *Fleet) remove(id model.JobID) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.running, id)
	delete(f.stopped, id)
}

// youngest is the job that started last among the running jobs, and false when none runs.
func (f *Fleet) youngest() (model.JobID, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var (
		best  model.JobID
		order uint64
	)

	for id, n := range f.running {
		if n > order {
			best, order = id, n
		}
	}

	return best, order > 0
}

func (f *Fleet) markStopped(id model.JobID) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.stopped[id] = true
}

func (f *Fleet) wasStopped(id model.JobID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.stopped[id]
}

// Wait is the Wait of the host. At each poll the wait of the youngest job of the fleet reads the free space with DiskFloor.
// When the host is under its floor, that wait stops its job, and the state of the job is JobDiskFull: a problem of the host, not a result of the job.
// A disk that cannot be read stops no job. A guard with no poll does not watch the disk.
func (f floored) Wait(ctx context.Context, id model.JobID) (model.JobState, error) {
	defer f.fleet.remove(id)

	if f.poll <= 0 {
		return f.Host.Wait(ctx, id)
	}

	tick, end := context.WithCancel(ctx)
	defer end()

	stop := make(chan error, 1)

	go func() { stop <- f.watch(tick, ctx, id) }()

	state, err := f.Host.Wait(ctx, id)

	end()

	return f.afterStop(id, state, err, <-stop)
}

// afterStop gives the end of a wait that the disk watch may have stopped. A job that the watch canceled ends as JobDiskFull.
// A stop that failed joins the error, because the job may still run.
func (f floored) afterStop(id model.JobID, state model.JobState, err, stopErr error) (model.JobState, error) {
	if stopErr != nil {
		err = errors.Join(err, fmt.Errorf("job %s: the host is under its disk floor and the stop of the job failed, so it may still run: %w", id, stopErr))
	}

	if state == model.JobCanceled && f.fleet.wasStopped(id) {
		state = model.JobDiskFull
	}

	return state, err
}

// watch reads the disk at each poll for the job id while it is the youngest of the fleet, until tick ends. It stops the job when the host is under its floor.
// The stop runs on ctx, not on tick, so the end of the wait does not cut the stop.
func (f floored) watch(tick, ctx context.Context, id model.JobID) error {
	for {
		select {
		case <-tick.Done():
			return nil
		case <-time.After(f.poll):
		}

		if youngest, ok := f.fleet.youngest(); !ok || youngest != id {
			continue
		}

		if !errors.Is(DiskFloor(tick, f.Host, f.floorGB), ErrDiskLow) {
			continue
		}

		f.fleet.markStopped(id)

		return f.Cancel(ctx, id)
	}
}
