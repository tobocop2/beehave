// Package hosttest holds the conformance suite for host.Host. An adapter test runs the suite against the adapter with fake command lines.
// Every new host provider must call the suite from its tests.
package hosttest

import (
	"context"
	"fmt"
	"path"
	"testing"
	"time"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
)

// Reporter is the part of testing.T the suite uses. A test of the suite passes a recorder to see a failure.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// Mode is how the fake behind a Factory behaves.
type Mode int

// The modes of a fake host. Working is the normal one. Broken makes every command fail.
// UnreadableDisk makes the disk reading of the host something that cannot be read.
const (
	Working Mode = iota
	Broken
	UnreadableDisk
)

// Factory returns a new host over fakes. The host starts absent.
// The fake runs a job whose command is PassingCommand to a succeeded state and a job whose command is FailingCommand to a failed one.
// A job whose command is UnknownCommand gets a state from the scheduler that obee does not know.
type Factory func(t *testing.T, mode Mode) host.Host

// The job commands that the fake behind a Factory knows.
const (
	PassingCommand = "pass"
	FailingCommand = "fail"
	UnknownCommand = "mystery"
)

// TestHost runs the conformance suite against the host that newHost returns, as testing/fstest.TestFS does for a file system.
func TestHost(t *testing.T, newHost Factory) {
	t.Helper()
	t.Run("runs_a_job_through_the_lifecycle", func(t *testing.T) {
		CheckHostWorks(t, newHost(t, Working))
	})
	t.Run("a_failed_command_is_an_error_everywhere", func(t *testing.T) {
		CheckHostFails(t, newHost(t, Broken))
	})
	t.Run("a_host_that_is_not_up_takes_no_job", func(t *testing.T) {
		CheckBeforeUp(t, newHost(t, Working))
	})
	t.Run("a_second_down_leaves_the_host_absent", func(t *testing.T) {
		CheckDownTwice(t, newHost(t, Working))
	})
	t.Run("a_disk_that_cannot_be_read_is_not_a_reading", func(t *testing.T) {
		CheckUnreadableDisk(t, newHost(t, UnreadableDisk))
	})
	t.Run("a_state_obee_does_not_know_is_not_final", func(t *testing.T) {
		CheckUnknownState(t, newHost(t, Working))
	})
}

// CheckBeforeUp checks a host that Up has not made: Status reports it absent and Submit is an error.
func CheckBeforeUp(r Reporter, h host.Host) {
	r.Helper()

	c := check{r: r}
	ctx, cancel := deadline()
	defer cancel()

	c.equal(status(ctx, c, h).State, model.HostAbsent, "state before Up")

	_, err := h.Submit(ctx, model.JobSpec{Name: "x", Command: PassingCommand})
	c.isError(err, "Submit before Up")
}

// CheckDownTwice checks that Status reports the host absent after a second Down. The second Down may succeed or fail.
func CheckDownTwice(r Reporter, h host.Host) {
	r.Helper()

	c := check{r: r}
	ctx, cancel := deadline()
	defer cancel()

	c.noError(h.Up(ctx), "Up")
	c.noError(h.Down(ctx), "first Down")

	err := h.Down(ctx)

	c.equal(status(ctx, c, h).State, model.HostAbsent, fmt.Sprintf("state after a second Down, which returned %v", err))
}

// CheckUnreadableDisk checks that a host whose disk cannot be read never reports a readable disk. Status may fail or report the reading as not readable.
func CheckUnreadableDisk(r Reporter, h host.Host) {
	r.Helper()

	c := check{r: r}
	ctx, cancel := deadline()
	defer cancel()

	c.noError(h.Up(ctx), "Up")

	if got, err := h.Status(ctx); err == nil && got.Disk.Readable {
		r.Errorf("a disk that cannot be read: Status reports a readable reading of %d bytes", got.Disk.FreeBytes)
	}
}

// CheckUnknownState checks that Wait never returns a final state for a job whose scheduler state obee does not know. Wait may fail or return a state that is not final.
func CheckUnknownState(r Reporter, h host.Host) {
	r.Helper()

	c := check{r: r}
	ctx, cancel := deadline()
	defer cancel()

	c.noError(h.Up(ctx), "Up")

	id, err := h.Submit(ctx, model.JobSpec{Name: "unknown", Command: UnknownCommand})
	c.noError(err, "Submit")

	if state, err := h.Wait(ctx, id); err == nil && state.Terminal() {
		r.Errorf("an unknown scheduler state: Wait returned the final state %q", state)
	}
}

// checkTimeout is the longest one check may run. A host that never reaches a final state fails the check here instead of hanging it.
const checkTimeout = time.Minute

func deadline() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), checkTimeout)
}

// check records a failed expectation.
type check struct {
	r Reporter
}

func (c check) noError(err error, what string) {
	c.r.Helper()

	if err != nil {
		c.r.Errorf("%s: unexpected error: %v", what, err)
	}
}

func (c check) isError(err error, what string) {
	c.r.Helper()

	if err == nil {
		c.r.Errorf("%s: want an error from a failed command, got none", what)
	}
}

func (c check) equal(got, want any, what string) {
	c.r.Helper()

	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		c.r.Errorf("%s: got %v, want %v", what, got, want)
	}
}

// CheckHostWorks checks the lifecycle of an absent host: up, a passing and a failing job, drain, resume, down.
func CheckHostWorks(r Reporter, h host.Host) {
	r.Helper()

	c := check{r: r}
	ctx, cancel := deadline()
	defer cancel()

	c.equal(status(ctx, c, h).State, model.HostAbsent, "state before Up")
	c.noError(h.Up(ctx), "Up")

	checkUp(ctx, c, h)

	c.equal(finish(ctx, c, h, PassingCommand), model.JobSucceeded, "state of a passing job")
	c.equal(finish(ctx, c, h, FailingCommand), model.JobFailed, "state of a failing job")

	checkDrain(ctx, c, h)
	checkCancel(ctx, c, h)

	c.noError(h.Down(ctx), "Down")
	c.equal(status(ctx, c, h).State, model.HostAbsent, "state after Down")
}

// checkUp checks a host that Up made: it is up, its disk is readable and its work root is one path.
func checkUp(ctx context.Context, c check, h host.Host) {
	c.r.Helper()

	up := status(ctx, c, h)
	c.equal(up.State, model.HostUp, "state after Up")
	c.equal(up.Disk.Readable, true, "the disk reading is readable")

	checkWorkRoot(ctx, c, h)
}

// checkWorkRoot checks that the work root is one absolute path, the same in every call, as Wait and Teardown must scan one root.
func checkWorkRoot(ctx context.Context, c check, h host.Host) {
	c.r.Helper()

	root, err := h.WorkRoot(ctx)
	c.noError(err, "WorkRoot")

	if !path.IsAbs(root) {
		c.r.Errorf("WorkRoot: got %q, want one absolute path", root)
	}

	again, err := h.WorkRoot(ctx)
	c.noError(err, "second WorkRoot")
	c.equal(again, root, "the work root in a second call")
}

func checkDrain(ctx context.Context, c check, h host.Host) {
	c.r.Helper()

	c.noError(h.Drain(ctx), "Drain")
	c.equal(status(ctx, c, h).State, model.HostDraining, "state after Drain")
	c.noError(h.Resume(ctx), "Resume")
	c.equal(status(ctx, c, h).State, model.HostUp, "state after Resume")
}

func checkCancel(ctx context.Context, c check, h host.Host) {
	c.r.Helper()

	id, err := h.Submit(ctx, model.JobSpec{Name: "cancel", Command: PassingCommand})
	c.noError(err, "Submit")
	c.noError(h.Cancel(ctx, id), "Cancel")

	state, err := h.Wait(ctx, id)
	c.noError(err, "Wait for a canceled job")
	c.equal(state, model.JobCanceled, "state of a canceled job")
}

func status(ctx context.Context, c check, h host.Host) model.HostStatus {
	c.r.Helper()

	got, err := h.Status(ctx)
	c.noError(err, "Status")

	return got
}

func finish(ctx context.Context, c check, h host.Host, command string) model.JobState {
	c.r.Helper()

	id, err := h.Submit(ctx, model.JobSpec{Name: command, Command: command})
	c.noError(err, "Submit "+command)

	state, err := h.Wait(ctx, id)
	c.noError(err, "Wait for "+command)

	return state
}

func checkJobCallsFail(ctx context.Context, c check, h host.Host) {
	c.r.Helper()

	_, err := h.Wait(ctx, "1")
	c.isError(err, "Wait")

	_, err = h.WorkRoot(ctx)
	c.isError(err, "WorkRoot")
}

// CheckHostFails checks that every method returns an error when its command fails.
func CheckHostFails(r Reporter, h host.Host) {
	r.Helper()

	c := check{r: r}
	ctx, cancel := deadline()
	defer cancel()

	c.isError(h.Up(ctx), "Up")

	_, err := h.Status(ctx)
	c.isError(err, "Status")

	_, err = h.Submit(ctx, model.JobSpec{Name: "x", Command: PassingCommand})
	c.isError(err, "Submit")

	checkJobCallsFail(ctx, c, h)

	c.isError(h.Cancel(ctx, "1"), "Cancel")
	c.isError(h.Drain(ctx), "Drain")
	c.isError(h.Resume(ctx), "Resume")
	c.isError(h.Down(ctx), "Down")
}
