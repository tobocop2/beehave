package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/host"
)

// jobUnits is a host that holds one job for each job key. start counts the jobs that the host starts: one systemd-run call on ssh, one sbatch on Slurm.
// A submit with the key of a job that the host still holds starts nothing. The first failing jobs that the host starts fail and every later job passes.
// keepsEnded is Slurm: accounting keeps an ended job, so its key stays held. On ssh the wait releases the unit after it banks the result.
type jobUnits struct {
	mu         sync.Mutex
	keepsEnded bool
	failing    int
	held       map[string]int
	starts     []string
	calls      int
	last       string
	crashAt    int
	dying      bool
	crash      func()
}

func newJobUnits(keepsEnded bool, failing, crashAt int, crash func()) *jobUnits {
	return &jobUnits{keepsEnded: keepsEnded, failing: failing, held: map[string]int{}, crashAt: crashAt, crash: crash}
}

// submit starts the job of key when the host does not hold it. The crash of the engine comes right after the submit of call number crashAt,
// before the wait banks anything, which is where a crash leaves a unit that is held and a step that has no record.
func (u *jobUnits) submit(key string) {
	u.mu.Lock()
	u.calls++

	if _, held := u.held[key]; !held {
		u.starts = append(u.starts, key)
		rc := 0

		if len(u.starts) <= u.failing {
			rc = 1
		}

		u.held[key] = rc
	}

	u.last = key
	crashNow := u.calls == u.crashAt
	u.dying = crashNow
	u.mu.Unlock()

	if crashNow {
		u.crash()
	}
}

// wait returns the exit code of the last job that submit named. The wait of the crashed engine banks nothing and releases nothing.
func (u *jobUnits) wait() int {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.dying {
		u.dying = false

		return 0
	}

	rc := u.held[u.last]
	if !u.keepsEnded {
		delete(u.held, u.last)
	}

	return rc
}

// install makes the fetch program of the harness a job of this host.
func (u *jobUnits) install(h *harness) {
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Run[0] == "fetch" {
			u.submit(host.JobKey(ctx))
		}

		return nil
	}
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] != "fetch" {
			return CommandOutput{}, nil
		}

		return CommandOutput{RC: u.wait()}, nil
	}
}

func (u *jobUnits) heldAtEnd() int {
	u.mu.Lock()
	defer u.mu.Unlock()

	return len(u.held)
}

func (u *jobUnits) startedKeys() []string {
	u.mu.Lock()
	defer u.mu.Unlock()

	return append([]string(nil), u.starts...)
}

// retriedFetch is a call that retries a failed fetch, and logs the exit code and the count of tries that the call returned.
func retriedFetch(retries string) string {
	return "c = command(run = [\"fetch\"], retries = " + retries + ", backoff = \"1ms\", name = \"fetch\")\ncommand(run = [\"log\", str(c.rc), str(c.attempts)], name = \"log\")\n"
}

func TestCommandRetries_ACrashDuringTry2OnSshDoesNotStartTry1Again(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	crash := newCrash(t, h)
	units := newJobUnits(false, 1, 2, crash.after)
	units.install(h)

	crashAndResume(t, h, h.deps(), retriedFetch("1"))

	keys := units.startedKeys()
	assert.Len(t, keys, 2, "try 1 and try 2 each start one unit, and the resume starts none: the unit of try 2 is still held")
	assert.Len(t, uniqueOf(keys), len(keys), "no job key starts twice")
	assert.Equal(t, "log 0 2", logged(h), "the resume waits for try 2, which passed, and the call counts two tries")
	assert.Zero(t, units.heldAtEnd(), "no unit is left that no step waits on")
}

func TestCommandRetries_ACrashDuringTry3OnSshDoesNotStartAnEarlierTryAgain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	crash := newCrash(t, h)
	units := newJobUnits(false, 2, 3, crash.after)
	units.install(h)

	crashAndResume(t, h, h.deps(), retriedFetch("3"))

	keys := units.startedKeys()
	assert.Len(t, keys, 3, "one unit for each of the three tries, and none for the resume")
	assert.Len(t, uniqueOf(keys), len(keys), "no job key starts twice")
	assert.Equal(t, "log 0 3", logged(h))
	assert.Zero(t, units.heldAtEnd())
}

func TestCommandRetries_ACrashDuringTry2OnSlurmStartsNoJobTwice(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	crash := newCrash(t, h)
	units := newJobUnits(true, 1, 2, crash.after)
	units.install(h)

	crashAndResume(t, h, h.deps(), retriedFetch("1"))

	keys := units.startedKeys()
	assert.Len(t, keys, 2, "accounting keeps the ended job of try 1, so the resume starts nothing")
	assert.Len(t, uniqueOf(keys), len(keys))
	assert.Equal(t, "log 0 2", logged(h))
}

func TestCommandRetries_ACrashDuringTry2ResumesAtTry2AndKeepsTheTriesWithinTheRetryPolicy(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	crash := newCrash(t, h)
	calls := 0
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] != "fetch" {
			return CommandOutput{}, nil
		}

		calls++
		if calls == 2 {
			crash.after()
		}

		return CommandOutput{RC: 1}, nil
	}

	crashAndResume(t, h, h.deps(), retriedFetch("1"))

	assert.Equal(t, 3, timesRun(h, "fetch"), "try 1, try 2 that the crash cut short, and try 2 again: the resume does not run try 1 and try 2 anew")
	assert.Equal(t, "log 1 2", logged(h), "every try failed, and the call counts two tries")
}

func uniqueOf(keys []string) map[string]bool {
	seen := map[string]bool{}
	for _, key := range keys {
		seen[key] = true
	}

	return seen
}
