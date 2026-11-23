package flow

import (
	"context"
	"os"
	"path/filepath"
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

// localExampleFile copies examples/local beside a fresh folder and returns the path of its workflow file. The file holds the text of before when before is set,
// the workflow of the example before it used patched, as the committed fixture keeps it.
func localExampleFile(t *testing.T, before bool) string {
	t.Helper()

	folder := filepath.Join(t.TempDir(), "local")
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", "local"))))

	file := filepath.Join(folder, "workflow.star")

	if before {
		old, err := os.ReadFile(filepath.Join("testdata", "local_before_patched.star"))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(file, old, 0o600)) //nolint:gosec // the file lies in the temp folder of this test
	}

	return file
}

func TestPatched_AnOpenRunOfTheOldLocalExampleReplaysAfterAdoptionWithNoUnexpectedStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", costing(1))
	h := rig.h
	h.agents.hangAt = 3 // the review agent holds, so the run is open after it recorded the plan and implement steps
	file := localExampleFile(t, true)

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	go func() { _, _ = first.RunFile(testItem, testProject, file, modeOff()) }() //nolint:errcheck // the stop of the engine ends the run, and the test reads the store

	waitOn(t, conditionOf(runOfEngine(t, first), func() bool { return len(h.agents.seen()) >= 3 }))
	stop()
	require.NoError(t, first.Shutdown())
	expireLease(h)

	// The author installs the example as it is now over the open run.
	current, err := os.ReadFile(localExampleFile(t, false))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, current, 0o600)) //nolint:gosec // the file lies in the temp folder of this test

	h.agents.hangAt = 0
	client := h.client(t, testVersion)
	require.NoError(t, client.ResumeOnThisFile(testItem))

	engine := h.engine(t)

	waitOn(t, conditionOf(runOfEngine(t, engine), func() bool {
		last, found, readErr := latestRun(engine.store(), testItem)

		return readErr == nil && found && last.terminal()
	}))

	last, found, err := latestRun(engine.store(), testItem)
	require.NoError(t, err)
	require.True(t, found)
	require.Error(t, last.Status.Error, "the fake review agent writes no verdict, so the run ends after the replay")
	assert.NotContains(t, last.Status.Error.Error(), "UnexpectedStep", "the replay of the recorded steps meets no other step")

	names, err := client.stepNames(testRun)
	require.NoError(t, err)
	assert.Equal(t, 1, stepCount(names, "agent:implement#2/start"), "the recorded implement step replays and does not run again: %v", names)
	assert.NotContains(t, names, "DBOS.patch-implement-base", "a run that passed the call recorded no marker")
	assert.NotContains(t, names, "facts#1", "the old branch does not read the facts")
	assert.Contains(t, names, "gate:check#1", "the run went on past the implement step")
}

func TestPatched_AnOpenRunOfTheOldLocalExampleThatRecordedAVoidGateReplaysAfterAdoptionWithNoUnexpectedStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", costing(1))
	h := rig.h
	h.gate.results = []GateResult{{Passed: false, Steps: []GateStep{{Name: "command", Status: "unknown"}}}, {Passed: true}}
	h.agents.hangAt = 3 // the old example sends the void gate to the fix agent, which holds, so the run is open after it recorded the gate
	file := localExampleFile(t, true)

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	go func() { _, _ = first.RunFile(testItem, testProject, file, modeOff()) }() //nolint:errcheck // the stop of the engine ends the run, and the test reads the store

	waitOn(t, conditionOf(runOfEngine(t, first), func() bool { return len(h.agents.seen()) >= 3 }))
	stop()
	require.NoError(t, first.Shutdown())
	expireLease(h)

	current, err := os.ReadFile(localExampleFile(t, false))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, current, 0o600)) //nolint:gosec // the file lies in the temp folder of this test

	h.agents.hangAt = 0
	client := h.client(t, testVersion)
	require.NoError(t, client.ResumeOnThisFile(testItem))

	engine := h.engine(t)

	waitOn(t, conditionOf(runOfEngine(t, engine), func() bool {
		last, found, readErr := latestRun(engine.store(), testItem)

		return readErr == nil && found && last.terminal()
	}))

	last, found, err := latestRun(engine.store(), testItem)
	require.NoError(t, err)
	require.True(t, found)
	require.Error(t, last.Status.Error, "the fake review agent writes no verdict, so the run ends after the replay")
	assert.NotContains(t, last.Status.Error.Error(), "UnexpectedStep", "the replay of the recorded void gate and fix step meets no other step")

	names, err := client.stepNames(testRun)
	require.NoError(t, err)
	assert.Equal(t, 1, stepCount(names, "gate:check#1"), "the recorded void gate replays and does not run again: %v", names)
	assert.NotContains(t, names, "DBOS.patch-void-gate", "a run that passed the call recorded no marker")
}

// stepCount counts the entries of names that equal want.
func stepCount(names []string, want string) int {
	return len(names) - len(slices.DeleteFunc(slices.Clone(names), func(name string) bool { return name == want }))
}

func TestPatched_ANameThatTheSignatureCannotSeeIsRefusedWhenTheRunReachesTheCall(t *testing.T) {
	t.Parallel()

	_, err := walkBody(t, newHarness(t), "name = \"a\" + \":b\"\npatched(name)")

	require.ErrorContains(t, err, `patched: name "a:b" holds a character other than`)
}

func TestPatched_ANameThatIsNoTextIsRefusedWhenTheRunReachesTheCall(t *testing.T) {
	t.Parallel()

	_, err := walkBody(t, newHarness(t), "name = 3 + 4\npatched(name)")

	require.ErrorContains(t, err, "patched")
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
