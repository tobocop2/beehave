package flow

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
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

func TestCommandRetries_TheJobKeyOfEachTryIsTheRunAndTheIDOfTheStepOfThatTry(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	jobs := hostJobs(h)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("command(run = [\"fetch\"], retries = 1, backoff = \"1ms\", name = \"fetch\")\n")), modeOff())
	require.NoError(t, err)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	var tryKeys []string

	for _, step := range steps {
		if strings.HasPrefix(step.StepName, "command:fetch#1/try") {
			tryKeys = append(tryKeys, testRun+":"+strconv.Itoa(step.StepID))
		}
	}

	require.Len(t, tryKeys, 2, "one recorded step for each try")
	assert.Equal(t, tryKeys, jobs.order, "each try submits its job under the key of its own step")
}
