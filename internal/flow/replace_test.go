package flow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/workflow"
)

// replaceRuns lists the runs of an item as the store view reads them, oldest first.
func replaceRuns(t *testing.T, c *Client, item string) []run {
	t.Helper()

	rows, err := c.store().List(dbos.WithFilterWorkflowIDPrefix(runPrefix+item+attemptMarker), dbos.WithFilterName(itemName), dbos.WithFilterLoadInput(true))
	require.NoError(t, err)

	out := make([]run, 0, len(rows))
	for _, row := range rows {
		out = append(out, run{Status: row})
	}

	slices.SortFunc(out, func(a, b run) int { return strings.Compare(a.Status.ID, b.Status.ID) })

	return out
}

// replaceStates lists the id and status of each run of the item, for a failure message.
func replaceStates(runs []run) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.Status.ID+"="+string(r.Status.Status))
	}

	return out
}

// staysTrue reads the fact for a short time and fails when it is false at any read. The loop ends before the test does, so no read outlives the store.
func staysTrue(t *testing.T, fact func() bool, what string) {
	t.Helper()

	for end := time.Now().Add(500 * time.Millisecond); time.Now().Before(end); time.Sleep(pollEvery) {
		require.True(t, fact(), what)
	}
}

// refusalWait is how long a test waits for a start that must be refused at once. A start that is not refused waits for the run it began, so the wait ends the test as a failure.
const refusalWait = 5 * time.Second

// refusedAtOnce returns the outcome of a start that must end at once, and fails the test when the start is still running after refusalWait.
func refusedAtOnce(t *testing.T, done <-chan runOutcome) runOutcome {
	t.Helper()

	select {
	case result := <-done:
		return result
	case <-time.After(refusalWait):
		require.FailNow(t, "the start was not refused: it started a run and waits for it")
	}

	return runOutcome{}
}

// startWith runs the default workflow file of the harness for the item with the start option, in the background.
func startWith(engine *Engine, item, project string, policy workflow.OnOpen) <-chan runOutcome {
	return startFile(engine, item, project, filepath.Join(filepath.Dir(engine.dir), defaultFlowName), RunOnOpen(policy))
}

// startFile runs the workflow file for the item in the background and sends the outcome.
func startFile(engine *Engine, item, project, file string, options ...RunOption) <-chan runOutcome {
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(item, project, file, append([]RunOption{modeOff()}, options...)...)
		done <- runOutcome{result: result, err: err}
	}()

	return done
}

// openRunRig starts the first run of the item on a flow that waits for the owner, and returns when it shows its question.
func openRunRig(t *testing.T) (*harness, *Engine, *Client) {
	t.Helper()

	h := newHarness(t)
	h.flowIs(t, waitFlow)

	engine := h.engine(t)
	client := h.client(t, testVersion)

	runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, h))

	return h, engine, client
}

func TestReplace_AStartWithAnOpenRunCancelsItAndStartsOneNewAttempt(t *testing.T) {
	t.Parallel()

	_, engine, client := openRunRig(t)

	startWith(engine, testItem, testProject, workflow.OnOpenReplace)

	soon(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 2 && runs[0].Status.Status == dbos.WorkflowStatusCancelled && !runs[1].terminal()
	}, "the first run is canceled and attempt 2 is open")

	assert.Equal(t, "item-12-r2", replaceRuns(t, client, testItem)[1].Status.ID)
}

func TestReplace_WithNoOpenRunStartsAttemptOneAndCancelsNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, waitFlow)

	engine := h.engine(t)
	client := h.client(t, testVersion)

	startWith(engine, testItem, testProject, workflow.OnOpenReplace)
	waitOn(t, ownerAsks(t, h))

	runs := replaceRuns(t, client, testItem)

	require.Len(t, runs, 1, replaceStates(runs))
	assert.Equal(t, testRun, runs[0].Status.ID)
	assert.False(t, runs[0].terminal())
	assert.Empty(t, h.life.releasedBy(), "nothing was canceled, so nothing was released")
}

func TestReplace_ARunThatEndedBeforeTheReadUnderTheLockIsNotCanceledAndTheNextAttemptStarts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	result := <-startWith(engine, testItem, testProject, workflow.OnOpenReplace)

	require.NoError(t, result.err)

	runs := replaceRuns(t, client, testItem)
	require.Len(t, runs, 2, replaceStates(runs))
	assert.Equal(t, dbos.WorkflowStatusSuccess, runs[0].Status.Status, "a finished run is never canceled")
}

func TestReplace_AFailedRunIsRefusedAsWithNoOptionAndNothingIsCanceled(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.agents.err = errFake

	engine := h.engine(t)
	client := h.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	result := <-startWith(engine, testItem, testProject, workflow.OnOpenReplace)

	require.ErrorIs(t, result.err, errRunFailed)

	runs := replaceRuns(t, client, testItem)
	require.Len(t, runs, 1, replaceStates(runs))
	assert.Equal(t, dbos.WorkflowStatusError, runs[0].Status.Status)
}

func TestReplace_ARunOfAnotherProjectIsRefusedBeforeTheCancel(t *testing.T) {
	t.Parallel()

	_, engine, client := openRunRig(t)

	result := refusedAtOnce(t, startWith(engine, testItem, "other", workflow.OnOpenReplace))

	require.ErrorIs(t, result.err, errProjectConflict)

	runs := replaceRuns(t, client, testItem)
	require.Len(t, runs, 1, replaceStates(runs))
	assert.False(t, runs[0].terminal(), "the run of the other project stays open")
}

func TestReplace_AJoinForAnotherProjectIsRefusedBeforeTheCancel(t *testing.T) {
	t.Parallel()

	h, _, client := openRunRig(t)

	ctx, cancel := context.WithTimeout(t.Context(), refusalWait)
	defer cancel()

	_, err := client.JoinRun(ctx, testItem, "other", filepath.Join(filepath.Dir(h.opts.StateDir), defaultFlowName), modeOff(), RunOnOpen(workflow.OnOpenReplace))

	require.ErrorIs(t, err, errProjectConflict)

	runs := replaceRuns(t, client, testItem)
	require.Len(t, runs, 1, replaceStates(runs))
	assert.False(t, runs[0].terminal(), "the run of the other project stays open")
}

func TestReplace_ASpawnThatReplacesAChildOfAnotherProjectIsRefusedBeforeTheCancel(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, waitFlow)

	engine := h.engine(t)
	client := h.client(t, testVersion)

	startFile(engine, "21", "other", filepath.Join(filepath.Dir(h.opts.StateDir), defaultFlowName))

	soon(t, func() bool { return len(replaceRuns(t, client, "21")) == 1 }, "the child 21 is open under the project other")

	result := refusedAtOnce(t, startFile(engine, "13", testProject, writeFlow(t, replaceChildFlow)))

	require.ErrorIs(t, result.err, errProjectConflict)
	assert.False(t, replaceRuns(t, client, "21")[0].terminal(), "the child of the other project stays open")
}

func TestReplace_WaitsForTheHolderOfProbeLockThenCancelsTheRunItLeft(t *testing.T) {
	t.Parallel()

	_, engine, client := openRunRig(t)

	probe, err := holdProbe(engine.dir)
	require.NoError(t, err)

	startWith(engine, testItem, testProject, workflow.OnOpenReplace)

	staysTrue(t, func() bool { return !replaceRuns(t, client, testItem)[0].terminal() }, "the replace waits while another process holds probe.lock")

	require.NoError(t, probe.Unlock())

	soon(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 2 && runs[0].Status.Status == dbos.WorkflowStatusCancelled && !runs[1].terminal()
	}, "after the lock the run is canceled and attempt 2 starts")
}

func TestReplace_TheEngineReleasesOnlyTheTokenOfAttemptOneAndAttemptTwoTakesItsOwnClaim(t *testing.T) {
	t.Parallel()

	h, engine, client := openRunRig(t)

	startWith(engine, testItem, testProject, workflow.OnOpenReplace)

	soon(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 2 && !runs[1].terminal()
	}, "attempt 2 is open")
	h.tick(t)

	soon(t, func() bool { return len(h.life.releasedBy()) > 0 }, "the engine releases the canceled attempt 1")

	assert.Equal(t, []string{testOwner + testItem}, h.life.releasedBy(), "only the token of attempt 1 is released")

	soon(t, func() bool {
		h.life.mu.Lock()
		defer h.life.mu.Unlock()

		return h.life.takes[testItem] == 2
	}, "attempt 2 took its own claim after the release")
}

func TestReplace_AJoinWithTheOptionCancelsTheRunOfTheEngineAndWaitsForTheNewOne(t *testing.T) {
	t.Parallel()

	h, _, client := openRunRig(t)

	ctx, stop := context.WithCancel(t.Context())
	defer stop()

	joined := make(chan error, 1)

	go func() {
		_, err := client.JoinRun(ctx, testItem, testProject, filepath.Join(filepath.Dir(h.opts.StateDir), defaultFlowName), modeOff(), RunOnOpen(workflow.OnOpenReplace))
		joined <- err
	}()

	soon(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 2 && runs[0].Status.Status == dbos.WorkflowStatusCancelled && !runs[1].terminal()
	}, "the join canceled attempt 1 and queued attempt 2")
}

func TestReplace_AStartWithNoOptionStillJoinsTheOpenRun(t *testing.T) {
	t.Parallel()

	_, engine, client := openRunRig(t)

	startWith(engine, testItem, testProject, "")

	staysTrue(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 1 && !runs[0].terminal()
	}, "the open run is joined, not replaced")
}

func TestReplace_AnOptionThatIsNeitherJoinNorReplaceIsRefusedByTheEngineAndTheClientAndStartsNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	file := filepath.Join(filepath.Dir(h.opts.StateDir), defaultFlowName)

	_, err := engine.RunFile(testItem, testProject, file, modeOff(), RunOnOpen("swap"))
	require.EqualError(t, err, `on_open is "swap". Use "join" or "replace"`)

	_, err = client.JoinRun(t.Context(), testItem, testProject, file, modeOff(), RunOnOpen("swap"))
	require.EqualError(t, err, `on_open is "swap". Use "join" or "replace"`)

	assert.Empty(t, replaceRuns(t, client, testItem), "a refused start stores no run")
}

func TestReplace_TheOptionIsAStartInstructionAndTheStoredInputReadsItEmpty(t *testing.T) {
	t.Parallel()

	_, engine, client := openRunRig(t)

	text, err := json.Marshal(ItemInput{Item: testItem, OnOpen: workflow.OnOpenReplace})
	require.NoError(t, err)
	assert.NotContains(t, string(text), "replace")

	startWith(engine, testItem, testProject, workflow.OnOpenReplace)

	soon(t, func() bool { return len(replaceRuns(t, client, testItem)) == 2 }, "attempt 2 is stored")

	in, err := replaceRuns(t, client, testItem)[1].input()
	require.NoError(t, err)
	assert.Empty(t, in.OnOpen, "a resume or a replay never reads the start option")
}

const replaceChildFlow = `def worker(item):
    wait(name = "ask", question = "What now?")

def main(item):
    runs = spawn(items = ["21"], entry = "worker", on_open = "replace", name = "fan")
    gather(runs, name = "fan")
`

const firstParentFlow = `def worker(item):
    wait(name = "ask", question = "What now?")

def main(item):
    runs = spawn(items = ["21"], entry = "worker", name = "fan")
    gather(runs, name = "fan")
    command(name = "after", run = ["after"])
`

func TestReplace_ASpawnWithOnOpenReplaceCancelsTheOpenChildAndTellsItsParent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	startFile(engine, testItem, testProject, writeFlow(t, firstParentFlow))

	soon(t, func() bool {
		runs := replaceRuns(t, client, "21")

		return len(runs) == 1 && !runs[0].terminal()
	}, "the first parent started the child 21")

	startFile(engine, "13", testProject, writeFlow(t, replaceChildFlow))

	soon(t, func() bool {
		runs := replaceRuns(t, client, "21")

		return len(runs) == 2 && runs[0].Status.Status == dbos.WorkflowStatusCancelled && !runs[1].terminal()
	}, "the child of the first parent is canceled and attempt 2 is open")

	assert.Equal(t, "item-13-r1", replaceRuns(t, client, "21")[1].Status.ParentWorkflowID, "the new child belongs to the parent that replaced it")
	soon(t, func() bool { return slices.Contains(h.commands.ran(), "after") }, "the old parent reads the end of its child at once and goes on")
}

func TestReplace_ASpawnWithTheDefaultStillRefusesAnOpenChild(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	startFile(engine, testItem, testProject, writeFlow(t, firstParentFlow))

	soon(t, func() bool { return len(replaceRuns(t, client, "21")) == 1 }, "the first parent started the child 21")

	_, err := engine.RunFile("13", testProject, writeFlow(t, strings.Replace(replaceChildFlow, `on_open = "replace", `, "", 1)), modeOff())

	require.ErrorContains(t, err, "item 21 has an open run")
	assert.False(t, replaceRuns(t, client, "21")[0].terminal(), "the open child stays open")
}

func TestReplace_ASpawnWhoseOnOpenWordIsComputedAndIsNeitherJoinNorReplaceFailsAtTheCall(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	file := strings.Replace(replaceChildFlow, `on_open = "replace"`, `on_open = "sw" + "ap"`, 1)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.ErrorContains(t, err, `on_open is "swap". Use "join" or "replace"`)
}
