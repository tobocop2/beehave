// Package jobwait is the wait of a host adapter for a job that a scheduler runs. It reads the state of the job until it is final.
// It ends the wait as lost, stale or stuck when the scheduler gives no usable answer, and as died when a record stays empty.
// A final job has its work banked and its slot released. The states and their time limits are the same for every adapter.
package jobwait

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/link"
	"github.com/tobocop2/obee/internal/model"
)

var errCancelStuck = errors.New("the cancel did not take")

const (
	defaultStale = 20 * time.Minute
	lostAfter    = 5 * time.Minute // the time without an answered read before a wait ends as lost
	stopLimit    = 5 * time.Minute // the time a job may take to end after the cancel
	diedGrace    = time.Minute     // the time an empty record repeats before it reads as died: the accounting record can come late
)

// Job is what the job id of an adapter holds: the id that the scheduler made and the nonce that Submit made for the slot of the job.
type Job struct {
	ID    string
	Nonce string
}

// String is the job id that Submit returns. The adapter parses it back.
// A job with no slot has no nonce, and its id is the whole text.
func (j Job) String() string {
	if j.Nonce == "" {
		return j.ID
	}

	return j.ID + "-" + j.Nonce
}

// Scheduler is what an adapter gives the engine. A read that the ssh link drops returns an error that wraps link.ErrDropped.
type Scheduler interface {
	// Observe reads the state of the job and, when the job runs, the size of its log. A size of -1 means the log was not read or has no size.
	// A job that the scheduler does not know reads as died.
	Observe(ctx context.Context, job Job) (state model.JobState, size int64, err error)
	// Stop signals the job to end. It does not wait.
	Stop(ctx context.Context, job Job) error
	LostAdvice(job Job) string
	StuckAdvice(job Job) string
}

type Clock struct {
	Poll  time.Duration
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

type Engine struct {
	sched   Scheduler
	release func(ctx context.Context, job Job) error
	clock   Clock
	bank    func(ctx context.Context) error
	stale   time.Duration
}

// New returns the engine of a scheduler. bank banks the work of the host. release ends the slot of a job after the bank.
// stale is the time a running job may keep its log still; zero means the default.
func New(sched Scheduler, release func(ctx context.Context, job Job) error, clock Clock, bank func(ctx context.Context) error, stale time.Duration) *Engine {
	if stale <= 0 {
		stale = defaultStale
	}

	return &Engine{sched: sched, release: release, clock: clock, bank: bank, stale: stale}
}

// Wait polls the scheduler until the job has a final state. A state that obee does not know is an error, never a guess.
//
// A job holds its slot until its work is banked. Wait banks the work of the host, then releases the slot of the job.
// Work that cannot be banked is an error, and Wait returns the state of the job together with that error.
func (e *Engine) Wait(ctx context.Context, job Job) (model.JobState, error) {
	return e.waitFor(ctx, job, false)
}

// Cancel stops the job, waits until the scheduler reports it final, banks the work of the host and releases the slot, as Wait does.
// When the stop fails, the job may still run, so the slot keeps the host busy and Cancel returns the error.
func (e *Engine) Cancel(ctx context.Context, job Job) error {
	if err := e.sched.Stop(ctx, job); err != nil {
		return err
	}

	_, err := e.waitFor(ctx, job, true)

	return err
}

// waitFor is Wait. A stop wait follows a cancel: it has no stale check, as the stale stop would call itself, and it ends with an error
// when the job is not final after stopLimit on the clock of the host.
func (e *Engine) waitFor(ctx context.Context, job Job, stopping bool) (model.JobState, error) {
	w := waiting{grew: e.clock.Now(), answered: e.clock.Now(), size: -1}
	if stopping {
		w.stopBy = e.clock.Now().Add(stopLimit)
	}

	for {
		if end, over := e.pollOnce(ctx, job, &w); over {
			return end.state, end.err
		}

		if err := e.clock.Sleep(ctx, e.clock.Poll); err != nil {
			return model.JobUnknown, fmt.Errorf("wait for job %s: %w", job, err)
		}
	}
}

type ending struct {
	state model.JobState
	err   error
}

// waiting is what one Wait remembers between its polls: the time of the last answered read, the time an empty record was first read,
// and the last size of the log with the time it last grew.
type waiting struct {
	answered time.Time
	empty    time.Time
	size     int64
	grew     time.Time
	stopBy   time.Time // the end of a stop wait; zero for a wait with no stop
}

// pollOnce reads the job once and says whether the wait is over, with what Wait returns when it is.
func (e *Engine) pollOnce(ctx context.Context, job Job, w *waiting) (ending, bool) {
	state, err := e.observe(ctx, job, w)

	switch {
	case errors.Is(err, link.ErrDropped) && e.clock.Now().Sub(w.answered) >= lostAfter:
		return ending{model.JobLost, fmt.Errorf("job %s: no read of the job answered for %s (%w): %w. %s", job, lostAfter, err, host.ErrLost, e.sched.LostAdvice(job))}, true
	case err != nil && !errors.Is(err, link.ErrDropped):
		return ending{model.JobUnknown, err}, true
	case err == nil && state.Terminal():
		return endOf(e.banked(ctx, job, state)), true
	case err == nil:
		return e.unfinished(ctx, job, w)
	}

	return ending{}, false
}

// unfinished says whether the wait is over for a job that is not final. A stop wait ends at its limit; any other wait ends when the log is stale.
func (e *Engine) unfinished(ctx context.Context, job Job, w *waiting) (ending, bool) {
	switch {
	case !w.stopBy.IsZero() && !e.clock.Now().Before(w.stopBy):
		return ending{model.JobUnknown, fmt.Errorf("%w: job %s was not final %s after the cancel. %s", errCancelStuck, job, stopLimit, e.sched.StuckAdvice(job))}, true
	case w.stopBy.IsZero() && e.clock.Now().Sub(w.grew) >= e.stale:
		return endOf(e.stopStale(ctx, job)), true
	}

	return ending{}, false
}

func endOf(state model.JobState, err error) ending { return ending{state, err} }

// observe reads the state of the job once. A read that the ssh link drops returns link.ErrDropped; a read that answers restarts the lost clock.
// While the job runs it also reads the size of the log, which restarts the stale clock when the log grew.
// A job that runs counts as answered only when its log read answers too, so a link that drops every log read ends as lost.
func (e *Engine) observe(ctx context.Context, job Job, w *waiting) (model.JobState, error) {
	state, size, err := e.sched.Observe(ctx, job)
	if errors.Is(err, link.ErrDropped) && state != model.JobRunning {
		return model.JobUnknown, err
	}

	switch state = e.afterGrace(state, w); {
	case state != model.JobRunning:
		w.answered, w.grew = e.clock.Now(), e.clock.Now()
	case err == nil:
		w.answered = e.clock.Now()

		if size >= 0 && size != w.size {
			w.size, w.grew = size, e.clock.Now()
		}
	}

	return state, err
}

// afterGrace turns an empty record into died only after it repeats for diedGrace. Until then the job reads as pending, as the accounting record can come late.
// Any other state clears the empty read.
func (e *Engine) afterGrace(state model.JobState, w *waiting) model.JobState {
	if state != model.JobDied {
		w.empty = time.Time{}

		return state
	}

	if w.empty.IsZero() {
		w.empty = e.clock.Now()
	}

	if e.clock.Now().Sub(w.empty) < diedGrace {
		return model.JobPending
	}

	return model.JobDied
}

// stopStale stops a job whose log stopped growing, as Cancel does: it waits for the final state of the job, then banks its work and releases its slot.
// The state is stale, with the error of a stop that failed.
func (e *Engine) stopStale(ctx context.Context, job Job) (model.JobState, error) {
	if err := e.Cancel(ctx, job); err != nil {
		return model.JobStale, fmt.Errorf("job %s is stale and its stop did not finish, so it may still run: %w", job.ID, err)
	}

	return model.JobStale, nil
}

// banked banks the work of the host, releases the slot of the job, and returns the final state. The state comes back with every error.
func (e *Engine) banked(ctx context.Context, job Job, state model.JobState) (model.JobState, error) {
	id := job.ID

	if err := e.bank(ctx); err != nil && !errors.Is(err, host.ErrNoRepos) {
		return state, fmt.Errorf("job %s is %s but the host holds work that is not banked: %w", id, state, err)
	}

	if err := e.release(ctx, job); err != nil {
		return state, fmt.Errorf("job %s is %s and its work is banked, but the slot of the job cannot end: %w", id, state, err)
	}

	return state, nil
}
