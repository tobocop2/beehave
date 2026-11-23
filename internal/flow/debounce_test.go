package flow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

// debounceWindow is the window of the tests. It is long enough that a read right after a start sees the delayed run, and short enough that a test waits for the end of it.
const debounceWindow = 2 * time.Second

// debounceFiles writes one workflow file for each tag. The tag makes the bytes differ, so the pinned file of a run tells which start the run took.
func debounceFiles(t *testing.T, tags ...string) []string {
	t.Helper()

	files := make([]string, 0, len(tags))
	for _, tag := range tags {
		files = append(files, writeFlow(t, "def main(item):\n    wait(name = \"ask\", question = \"What now?\")\n# "+tag+"\n"))
	}

	return files
}

// debounceFileOf returns the file that the stored input of the run pins.
func debounceFileOf(t *testing.T, r run) string {
	t.Helper()

	in, err := r.input()
	require.NoError(t, err)

	return in.File
}

// debouncedRunIs reports whether the item has one run, in the status, with the id and the file.
func debouncedRunIs(t *testing.T, c *Client, status dbos.WorkflowStatusType, id, file string) bool {
	t.Helper()

	runs := replaceRuns(t, c, testItem)

	return len(runs) == 1 && runs[0].Status.Status == status && runs[0].Status.ID == id && debounceFileOf(t, runs[0]) == file
}

func TestDebounce_AStartWaitsForTheWindowAndThenRunsOnceUnderTheFirstAttemptId(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	files := debounceFiles(t, "only")

	began := time.Now()

	startFile(engine, testItem, testProject, files[0], RunDebounce(debounceWindow))

	soon(t, func() bool { return debouncedRunIs(t, client, dbos.WorkflowStatusDelayed, "item-12-r1", files[0]) }, "the start stores a delayed run under the id of attempt 1")

	soon(t, func() bool { return debouncedRunIs(t, client, dbos.WorkflowStatusPending, "item-12-r1", files[0]) }, "the run starts when the window ends")
	assert.GreaterOrEqual(t, time.Since(began), debounceWindow, "the run started before the window ended")

	in, err := replaceRuns(t, client, testItem)[0].input()
	require.NoError(t, err)
	assert.Zero(t, in.Debounce, "the window is an instruction of the start, so the store holds none")

	require.NoError(t, client.Cancel(testItem))
}

func TestDebounce_ABurstOfStartsFromTheEngineAndTheClientGivesOneRunWithTheInputOfTheLastStart(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	files := debounceFiles(t, "b1", "b2", "b3", "b4")

	ctx, stop := context.WithCancel(t.Context())
	defer stop()

	joined := make(chan error, len(files))

	for i, file := range files {
		if i%2 == 0 {
			startFile(engine, testItem, testProject, file, RunDebounce(debounceWindow))
		} else {
			go func() {
				_, err := client.JoinRun(ctx, testItem, testProject, file, modeOff(), RunDebounce(debounceWindow))
				joined <- err
			}()
		}

		soon(t, func() bool { return debouncedRunIs(t, client, dbos.WorkflowStatusDelayed, "item-12-r1", file) }, "the start took the place of the earlier start, and the run keeps the id of the first")
	}

	soon(t, func() bool { return debouncedRunIs(t, client, dbos.WorkflowStatusPending, "item-12-r1", files[3]) }, "one run starts, with the file of the last start")

	require.NoError(t, client.Cancel(testItem))

	for range 2 {
		// A join waits for the run that its bounce kept, so the cancel of that run ends it. Its error is the cancel.
		require.Error(t, <-joined)
	}
}

func TestDebounce_AStartAfterTheWindowMakesANewRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	files := debounceFiles(t, "early", "late")

	startFile(engine, testItem, testProject, files[0], RunDebounce(debounceWindow))
	soon(t, func() bool { return debouncedRunIs(t, client, dbos.WorkflowStatusPending, "item-12-r1", files[0]) }, "the first run started")
	require.NoError(t, client.Cancel(testItem))

	startFile(engine, testItem, testProject, files[1], RunDebounce(debounceWindow))

	soon(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 2 && runs[0].Status.Status == dbos.WorkflowStatusCancelled && runs[1].Status.ID == "item-12-r2" && runs[1].delayed() && debounceFileOf(t, runs[1]) == files[1]
	}, "the start after the run ended is a new attempt in its own window")

	require.NoError(t, client.Cancel(testItem))
}

func TestDebounce_AStartWithAWindowJoinsARunThatHasStarted(t *testing.T) {
	t.Parallel()

	_, engine, client := openRunRig(t)
	files := debounceFiles(t, "dropped")

	startFile(engine, testItem, testProject, files[0], RunDebounce(debounceWindow))

	staysTrue(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 1 && runs[0].Status.Status == dbos.WorkflowStatusPending
	}, "the run that started is joined: no delayed run, no new attempt")
}

func TestDebounce_AStartWithNoWindowStartsAtOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	files := debounceFiles(t, "now")

	startFile(engine, testItem, testProject, files[0], RunDebounce(0))

	soon(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 1 && runs[0].Status.Status == dbos.WorkflowStatusPending
	}, "the run starts with no delay")

	staysTrue(t, func() bool { return !replaceRuns(t, client, testItem)[0].delayed() }, "no window holds the run")

	require.NoError(t, client.Cancel(testItem))
}

func TestDebounce_AReplacingBurstCancelsTheStaleRunAtOnceAndLaterStartsBounceTheDelayedRun(t *testing.T) {
	t.Parallel()

	_, engine, client := openRunRig(t)
	files := debounceFiles(t, "r1", "r2", "r3")
	start := func(file string) {
		startFile(engine, testItem, testProject, file, RunOnOpen(workflow.OnOpenReplace), RunDebounce(debounceWindow))
	}

	start(files[0])

	soon(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 2 && runs[0].Status.Status == dbos.WorkflowStatusCancelled && runs[1].delayed() && debounceFileOf(t, runs[1]) == files[0]
	}, "the first start cancels the stale run at once, while the new run still waits for its window")

	for _, file := range files[1:] {
		start(file)

		soon(t, func() bool {
			runs := replaceRuns(t, client, testItem)

			return len(runs) == 2 && runs[1].Status.ID == "item-12-r2" && runs[1].delayed() && debounceFileOf(t, runs[1]) == file
		}, "a later start bounces the delayed run: it is not canceled, and it keeps its id")
	}

	soon(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 2 && runs[1].Status.Status == dbos.WorkflowStatusPending && debounceFileOf(t, runs[1]) == files[2]
	}, "one new run starts, with the input of the last start")

	require.NoError(t, client.Cancel(testItem))
}

func TestDebounce_AStartForAnotherProjectIsRefusedWhileTheWindowIsOpen(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	files := debounceFiles(t, "mine", "theirs")

	startFile(engine, testItem, testProject, files[0], RunDebounce(debounceWindow))
	soon(t, func() bool { return debouncedRunIs(t, client, dbos.WorkflowStatusDelayed, "item-12-r1", files[0]) }, "the first start holds its window")

	for _, policy := range []workflow.OnOpen{workflow.OnOpenJoin, workflow.OnOpenReplace} {
		ctx, cancel := context.WithTimeout(t.Context(), refusalWait)
		_, err := client.JoinRun(ctx, testItem, "other", files[1], modeOff(), RunOnOpen(policy), RunDebounce(debounceWindow))

		cancel()
		require.ErrorIs(t, err, errProjectConflict, policy)
	}

	assert.True(t, debouncedRunIs(t, client, dbos.WorkflowStatusDelayed, "item-12-r1", files[0]) || debouncedRunIs(t, client, dbos.WorkflowStatusPending, "item-12-r1", files[0]), "the refused starts changed nothing")
}

func TestDebounce_ANegativeWindowIsRefusedByTheEngineAndTheClientAndStartsNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	files := debounceFiles(t, "x")
	sentence := `debounce is "-1s", which must be above zero. Write a duration such as "30s", or leave debounce out to start at once`

	refused := refusedAtOnce(t, startFile(engine, testItem, testProject, files[0], RunDebounce(-time.Second)))
	require.EqualError(t, refused.err, sentence)

	ctx, cancel := context.WithTimeout(t.Context(), refusalWait)
	defer cancel()

	_, err := client.JoinRun(ctx, testItem, testProject, files[0], modeOff(), RunDebounce(-time.Second))
	require.EqualError(t, err, sentence)

	assert.Empty(t, replaceRuns(t, client, testItem), "a refused start stores no run")
}

func TestDebounce_AKeyThatAnotherRunHoldsIsReportedAndStartsNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	client := h.client(t, testVersion)
	files := debounceFiles(t, "x")

	in, err := pinnedInput(testItem, testProject, files[0], modeOff())
	require.NoError(t, err)

	// A run that no debounce made holds the key of the item on the roots queue.
	_, err = dbos.Enqueue[ItemResult](client.flow, rootsQueueName, itemName, in, dbos.WithEnqueueDeduplicationID(itemName+"-"+testItem), dbos.WithEnqueueApplicationVersion("another-build"))
	require.NoError(t, err)

	in.Debounce = debounceWindow
	_, err = client.debounce(in, 1)

	require.ErrorIs(t, err, dbos.ErrQueueDeduplicated)
	assert.Contains(t, err.Error(), "hold the start of item 12 for 2s")
}

func TestDebounce_AStreamOfStartsCannotHoldTheRunBackBeyondTenWindows(t *testing.T) {
	t.Parallel()

	const window = 200 * time.Millisecond

	h := newHarness(t)
	h.engine(t)
	client := h.client(t, testVersion)
	files := debounceFiles(t, "stream")

	in, err := pinnedInput(testItem, testProject, files[0], modeOff())
	require.NoError(t, err)

	in.Debounce = window
	began := time.Now()

	// Each pass is a later start in the window. The run must start although the stream goes on.
	for range 100 {
		if runs := replaceRuns(t, client, testItem); len(runs) == 1 && !runs[0].delayed() {
			break
		}

		_, err := client.debounce(in, 1)
		require.NoError(t, err)

		time.Sleep(window / 2)
	}

	runs := replaceRuns(t, client, testItem)
	require.Len(t, runs, 1, replaceStates(runs))
	assert.False(t, runs[0].delayed(), "the stream of starts held the run back past the cap")
	assert.GreaterOrEqual(t, time.Since(began), debounceHold*window, "the run started before the cap: the bounces did not push it back")

	require.NoError(t, client.Cancel(testItem))
}

func TestDebounce_TheWindowIsAnInstructionOfTheStartAndTheStoredInputReadsItEmpty(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(ItemInput{Item: testItem, Debounce: debounceWindow})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "ebounce")
	assert.NotContains(t, string(raw), "2000000000")
}
