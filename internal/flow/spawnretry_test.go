package flow

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retryFile is a parent that spawns three children with retries, gathers them, and records the outcome and the attempts of each in a last command.
func retryFile(gatherArgs string) string {
	return `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", retries = 1, name = "fan")
    done = gather(runs, ` + gatherArgs + `name = "fan")
    command(name = "after", run = ["after"] + [r.item + ":" + ("ok" if r.ok else "failed") + ":" + str(r.attempts) for r in done.results])
`
}

// failTimes makes the commands of one item fail for its first n commands. It returns the count of the commands of the item that started.
func failTimes(h *harness, item string, n int32) *atomic.Int32 {
	var seen atomic.Int32

	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Item == item && seen.Add(1) <= n {
			return errFake
		}

		return nil
	}

	return &seen
}

func TestSpawnRetries_AChildThatFailsOnceStartsAgainAsItsNextAttemptAndTheRaiseDoesNotFire(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tries := failTimes(h, "22", 1)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "raise", `)))

	require.NoError(t, err, "a failure with retries left does not raise")
	assert.Equal(t, []string{"after 21:ok:1 22:ok:2 23:ok:1"}, h.commands.ranBy(testItem))
	assert.Equal(t, int32(2), tries.Load(), "the child ran its command in each attempt")

	last, _, err := latestRun(engine.ctx, "22")
	require.NoError(t, err)
	assert.Equal(t, 2, last.Attempt, "the retry is the next attempt of the item")
	assert.Equal(t, testRun, last.Status.ParentWorkflowID, "the retry is a child of the parent")
	assert.Equal(t, []string{"spawn:fan#1", "gather:fan#1", "gather:fan#1.retry1", "gather:fan#1.1", "command:after#1"}, stepNames(t, engine))
}

func TestSpawnRetries_AChildThatFailsEachAttemptIsAFailedOutcomeAfterItsRetries(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tries := failTimes(h, "22", 5)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "return", `)))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 21:ok:1 22:failed:2 23:ok:1"}, h.commands.ranBy(testItem))
	assert.Equal(t, int32(2), tries.Load(), "one retry, then no more")
}

func TestSpawnRetries_ARaiseFiresWhenTheRetriesAreSpent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tries := failTimes(h, "22", 5)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "raise", `)))

	require.ErrorContains(t, err, errFake.Error())
	assert.Equal(t, int32(2), tries.Load())
	assert.Empty(t, h.commands.ranBy(testItem), "the raise ends the parent")
}

func TestSpawnRetries_ARetriedChildStartsAgainOnTheQueueOfItsDepth(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// Three slots let a retried run that lands on a wrong queue still run, so the test names the queue instead of waiting on a full one.
	h.opts.MaxConcurrent, h.opts.MaxReviewConcurrent = 3, 3

	var seen23, seen24 atomic.Int32

	h.commands.before = func(_ context.Context, request CommandRequest) error {
		switch {
		case request.Item == "23" && seen23.Add(1) == 1:
			return errFake
		case request.Item == "24" && seen24.Add(1) == 1:
			return errFake
		}

		return nil
	}

	engine := h.engine(t)
	body := `def l2(item):
    gather(spawn(items = ["23"], entry = "l3", retries = 1, name = "s3"), name = "g3")

def l3(item):
    command(name = "at", run = ["at"])
    gather(spawn(items = ["24"], entry = "l4", retries = 1, name = "s4"), name = "g4")

def l4(item):
    command(name = "leaf", run = ["leaf"])

def main(item):
    r = gather(spawn(items = ["21"], entry = "l2", name = "s2"), name = "g2").results[0]
    if not r.ok:
        fail(r.error)
`
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, body))
	require.NoError(t, err)

	for _, tc := range []struct {
		item  string
		depth int
	}{{"23", 2}, {"24", 3}} {
		in, status := inputOf(t, engine, tc.item)
		last, _, err := latestRun(engine.ctx, tc.item)
		require.NoError(t, err)
		assert.Equal(t, 2, last.Attempt, "item %s ran a second attempt", tc.item)
		assert.Equal(t, tc.depth, in.Depth, "item %s keeps its depth", tc.item)
		assert.Equal(t, itemsQueueOf(tc.depth), status.QueueName, "the second attempt of item %s goes on the queue of its depth", tc.item)
	}
}
