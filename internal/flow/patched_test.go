package flow

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The flows of the patched() tests. A change to the step after the question sits behind patched("greeting"): the new code reads the facts, the old code reads the clock.
const (
	patchedAskFirst = "def main(item):\n    wait(name = \"ask\", question = \"What now?\")\n    now()\n"
	patchedAskAfter = "def main(item):\n    wait(name = \"ask\", question = \"What now?\")\n    if patched(\"greeting\"):\n        facts()\n    else:\n        now()\n"
	patchedNowFirst = "def main(item):\n    now()\n    wait(name = \"ask\", question = \"What now?\")\n"
	patchedNowAfter = "def main(item):\n    if patched(\"greeting\"):\n        facts()\n    else:\n        now()\n    wait(name = \"ask\", question = \"What now?\")\n"
	patchedMarker   = "DBOS.patch-greeting"
)

// patchedAdopted moves the open run onto the file as it is now, lets the engine replay it, answers its question, and returns the names of the steps it recorded.
func patchedAdopted(t *testing.T, h *harness, client *Client) []string {
	t.Helper()

	require.NoError(t, client.ResumeOnThisFile(testItem))

	engine := h.engine(t)

	waitOn(t, waiting[struct{}]{what: "the replayed run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	waitOn(t, conditionOf(runOfEngine(t, engine), func() bool {
		last, found, err := latestRun(engine.store(), testItem)

		return err == nil && found && last.terminal()
	}))

	last, found, err := latestRun(engine.store(), testItem)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, runDone, stateOf(last.Status.Status), "the run ends done: %v", last.Status.Error)

	names, err := client.stepNames(testRun)
	require.NoError(t, err)

	return names
}

func TestPatched_AnOpenRunThatHasNotReachedTheCallTakesTheNewBranchAfterAdoption(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, patchedAskFirst)
	acc933Write(t, file, patchedAskAfter)

	names := patchedAdopted(t, h, h.client(t, testVersion))

	assert.Contains(t, names, patchedMarker, "the call recorded its marker")
	assert.Contains(t, names, "facts#1", "the run took the new branch")
	assert.NotContains(t, names, "now#1", "the old branch did not run")
}

func TestPatched_ARunThatPassedTheCallKeepsTheOldBranchAndReplaysWithNoUnexpectedStep(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, patchedNowFirst)
	acc933Write(t, file, patchedNowAfter)

	names := patchedAdopted(t, h, h.client(t, testVersion))

	assert.Contains(t, names, "now#1", "the recorded step of the old branch replays")
	assert.NotContains(t, names, "facts#1", "the new branch did not run")
	assert.NotContains(t, names, patchedMarker, "a run that passed the call records no marker")
}

func TestPatched_ARunThatStartsOnTheFileTakesTheNewBranch(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)
	file := writeFlow(t, patchedNowAfter)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the new run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	require.NoError(t, receive(t, done).err)

	names, err := client.stepNames(testRun)
	require.NoError(t, err)
	assert.Equal(t, 1, slices.Index(names, "facts#1")-slices.Index(names, patchedMarker), "the marker comes right before the step of the new branch")
	assert.NotContains(t, names, "now#1")
}

func TestPatched_ANameUsedTwiceInOneWorkflowIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := walkBody(t, newHarness(t), "patched(\"greeting\")\npatched(\"greeting\")")

	require.ErrorContains(t, err, `patched name "greeting" is used twice in this run`)
}

func TestPatched_ACallOutsideAWorkflowRunIsRefused(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	s := &script{e: engine, ctx: engine.ctx}

	require.ErrorIs(t, s.patchScope("greeting"), errNoScope)
}

func TestPatched_ANameWithACharacterOfAStepNameIsRefused(t *testing.T) {
	t.Parallel()

	_, err := walkBody(t, newHarness(t), "patched(\"a:b\")")

	require.ErrorContains(t, err, `patched: name "a:b" holds a character other than`)
}
