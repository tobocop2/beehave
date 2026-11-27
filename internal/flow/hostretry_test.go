package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host"
)

// keyedJobs is a host that remembers each job by its key, as Slurm does: a submit with the key of a job that ended gets that job and its exit code.
// The first job fails and every later job passes.
type keyedJobs struct {
	mu    sync.Mutex
	rc    map[string]int
	order []string
}

// run is the exit code of the job of the key in ctx. A new key starts a new job.
func (k *keyedJobs) run(ctx context.Context) int {
	k.mu.Lock()
	defer k.mu.Unlock()

	key := host.JobKey(ctx)
	if rc, found := k.rc[key]; found {
		return rc
	}

	rc := 0
	if len(k.order) == 0 {
		rc = 1
	}

	k.rc[key] = rc
	k.order = append(k.order, key)

	return rc
}

func hostJobs(h *harness) *keyedJobs {
	jobs := &keyedJobs{rc: map[string]int{}}

	var mu sync.Mutex

	rcs := map[string]int{}

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Run[0] == "fetch" {
			rc := jobs.run(ctx)

			mu.Lock()
			rcs[request.Item] = rc
			mu.Unlock()
		}

		return nil
	}
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		mu.Lock()
		defer mu.Unlock()

		if request.Run[0] != "fetch" {
			return CommandOutput{}, nil
		}

		return CommandOutput{RC: rcs[request.Item]}, nil
	}

	return jobs
}

func TestCommandRetries_ARetryOfAFailedHostJobStartsANewJobUnderANewKey(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	jobs := hostJobs(h)

	_, err := walkBody(t, h, "c = command(run = [\"fetch\"], retries = 1, backoff = \"1ms\", name = \"fetch\")\ncommand(run = [\"log\", str(c.rc), str(c.attempts)], name = \"log\")\n")

	require.NoError(t, err)
	require.Len(t, jobs.order, 2, "the retry started a second job and did not read the first one again")
	assert.NotEqual(t, jobs.order[0], jobs.order[1])
	assert.Equal(t, "log 0 2", logged(h), "the second job passed")
}

func TestAttemptKey_TheTriesOfOneStepHaveOneKeyEachAndAContextWithNoKeyHasNone(t *testing.T) {
	t.Parallel()

	keyed := host.WithJobKey(context.Background(), "run:3")

	assert.Equal(t, "run:3:1", host.JobKey(attemptKey(keyed, 1)))
	assert.Equal(t, "run:3:2", host.JobKey(attemptKey(keyed, 2)))
	assert.Equal(t, "run:3:1", host.JobKey(attemptKey(keyed, 1)), "a step that runs again after a crash counts from 1 and gets the key of the first try")
	assert.Empty(t, host.JobKey(attemptKey(context.Background(), 1)), "a call outside a run keeps no key, which starts a new job")
}
