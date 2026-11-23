package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// onThisFileRun leaves the run of the test item open at its question, stops the engine, and ends the lease of the run, so no holder protects it.
// The returned path is the workflow file of the run.
func onThisFileRun(t *testing.T, h *harness, text string) string {
	t.Helper()

	file := writeFlow(t, text)
	interrupted(t, h, file)
	expireLease(h)

	return file
}

// pinAttributeOf reads the pin that an adoption set on the run, as the store holds it. found is false when the run has none.
func pinAttributeOf(t *testing.T, c *Client, id string) (pin []workflow.PinnedFile, found bool) {
	t.Helper()

	row, err := c.row(id)
	require.NoError(t, err)

	if raw, set := row.Attributes[pinAttribute]; !set || raw == nil {
		return nil, false
	}

	pin, err = pinOf(row, ItemInput{})
	require.NoError(t, err)

	return pin, true
}

func TestClient_ResumeOnThisFileSetsThePinOfTheFileAsItIsNowAndReopensTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, acc933Frontier)
	acc933Write(t, file, acc933Frontier+"    now()\n")

	client := h.client(t, testVersion)
	require.NoError(t, client.ResumeOnThisFile(testItem))

	want, err := workflow.Pin(file)
	require.NoError(t, err)

	pin, found := pinAttributeOf(t, client, testRun)
	require.True(t, found, "the adoption wrote the pin on the run")
	assert.Equal(t, want, pin, "the pin is the file as it is on disk now")

	last, err := client.newest(testItem)
	require.NoError(t, err)
	assert.False(t, last.terminal(), "the run is open again: %s", last.Status.Status)
	require.NoError(t, requirePinned(testItem, last), "the pin check of a move reads the pin that the adoption wrote")
}

func TestClient_ResumeOnThisFileOfACanceledRunAdoptsTheFile(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, acc933Frontier)
	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	acc933Write(t, file, acc933Frontier+"    now()\n")

	require.NoError(t, client.ResumeOnThisFile(testItem))

	_, found := pinAttributeOf(t, client, testRun)
	assert.True(t, found)

	last, err := client.newest(testItem)
	require.NoError(t, err)
	assert.False(t, last.terminal(), "the canceled run is open again: %s", last.Status.Status)
}

func TestClient_ResumeOnThisFileOnAFileThatDidNotChangeIsAnOrdinaryResume(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	onThisFileRun(t, h, acc933Frontier)
	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))

	require.NoError(t, client.ResumeOnThisFile(testItem))

	_, found := pinAttributeOf(t, client, testRun)
	assert.False(t, found, "a file that did not change needs no pin")

	last, err := client.newest(testItem)
	require.NoError(t, err)
	assert.False(t, last.terminal(), "the run is open again: %s", last.Status.Status)
}

func TestClient_ResumeOnThisFileRefusesARunThatIsDone(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := writeFlow(t, acc933Plain)
	_, err := runFileWithin(t, h.engine(t), file)
	require.NoError(t, err)
	acc933Write(t, file, acc933Plain2)

	err = h.client(t, testVersion).ResumeOnThisFile(testItem)

	require.ErrorIs(t, err, ErrWrongState)
	require.ErrorContains(t, err, "only a cancelled, failed or open run moves") //nolint:misspell // user-facing state word
	require.ErrorContains(t, err, "beehave run "+testItem+" to start again")
}

func TestClient_ResumeOnThisFileRefusesAnItemWithNoRun(t *testing.T) {
	t.Parallel()

	err := newHarness(t).client(t, testVersion).ResumeOnThisFile(testItem)

	require.ErrorIs(t, err, errNoRun)
	require.ErrorContains(t, err, "start item "+testItem+" with beehave run "+testItem)
}

func TestClient_ResumeOnThisFileRefusesAnItemThatIsNoItemId(t *testing.T) {
	t.Parallel()

	err := newHarness(t).client(t, testVersion).ResumeOnThisFile("no good")

	require.ErrorIs(t, err, ErrBadItem)
}

func TestClient_ResumeOnThisFileRefusesARunOfAnotherBuildAndNamesTheBuildFlag(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	harness.flowIs(t, waitFlow+"\n# edited\n")
	expireLease(harness)

	err := other.ResumeOnThisFile(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, "beehave resume "+testItem+" --on-this-build")

	last, found, readErr := latestRun(other.store(), testItem)
	require.NoError(t, readErr)
	require.True(t, found)
	assert.False(t, last.terminal(), "a refused run is not canceled")
	assert.Equal(t, testVersion, buildVersion(last.Status.ApplicationVersion))
}

func TestClient_ResumeOnThisFileRefusesARunWhoseHolderStillRuns(t *testing.T) {
	t.Parallel()

	h, _, _ := strandedHarness(t)
	h.flowIs(t, waitFlow+"\n# edited\n")
	h.beats.setRenewed(time.Now())
	client := h.client(t, testVersion)

	err := client.ResumeOnThisFile(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, "claim lease")
	require.ErrorContains(t, err, "beehave resume "+testItem+" again with the same flags")

	_, found := pinAttributeOf(t, client, testRun)
	assert.False(t, found, "a refused adoption writes no pin")
}

func TestClient_ResumeOnThisFileShowsTheErrorOfAFileThatCannotBePinned(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, acc933Frontier)
	require.NoError(t, os.Remove(file))
	client := h.client(t, testVersion)

	err := client.ResumeOnThisFile(testItem)

	require.Error(t, err)
	require.ErrorContains(t, err, "workflow.star")

	_, found := pinAttributeOf(t, client, testRun)
	assert.False(t, found, "a file that cannot be read writes no pin")
	assert.False(t, run{Status: exactRun(t, client)}.terminal(), "the run is not canceled")
}

func TestClient_ResumeOnThisFileOfAnOpenRunThatCannotWriteThePinLeavesItCanceledWithNoPinAndSaysHowToRetry(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, acc933Frontier)
	acc933Write(t, file, acc933Frontier+"    now()\n")
	client := h.client(t, testVersion)
	spyOn(t, client).failPin = true

	err := client.ResumeOnThisFile(testItem)

	require.ErrorIs(t, err, errSpy)
	require.ErrorContains(t, err, "pin the workflow file for run "+testRun)
	require.ErrorContains(t, err, "beehave resume "+testItem+" --on-this-file again")
	assert.True(t, run{Status: exactRun(t, client)}.terminal(), "the pin is written after the cancel, so the run is canceled")

	_, found := pinAttributeOf(t, client, testRun)
	assert.False(t, found)
	requireNoMarks(t, client)
	requireMoveLockFree(t, client)
}

func TestClient_ResumeOnThisFileOfAnOpenRunWhoseCancelFailsWritesNoPin(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, acc933Frontier)
	acc933Write(t, file, acc933Frontier+"    now()\n")
	client := h.client(t, testVersion)
	spyOn(t, client).failCancel = true

	err := client.ResumeOnThisFile(testItem)

	require.ErrorIs(t, err, errSpy)
	assert.False(t, run{Status: exactRun(t, client)}.terminal(), "the run did not move")

	_, found := pinAttributeOf(t, client, testRun)
	assert.False(t, found, "a run that did not move carries no pin")
	requireNoMarks(t, client)
	requireMoveLockFree(t, client)
}

func TestClient_ResumeOnThisFileOfAnOpenRunOnAFileThatDidNotChangeReopensItWithNoPin(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	onThisFileRun(t, h, acc933Frontier)
	client := h.client(t, testVersion)

	require.NoError(t, client.ResumeOnThisFile(testItem))

	_, found := pinAttributeOf(t, client, testRun)
	assert.False(t, found, "a file that did not change needs no pin")

	last, err := client.newest(testItem)
	require.NoError(t, err)
	assert.False(t, last.terminal(), "the run is open again: %s", last.Status.Status)
	requireNoMarks(t, client)
	requireMoveLockFree(t, client)
}

func TestClient_ResumeOnThisFileOfACanceledRunThatCannotWriteThePinLeavesItCanceled(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, acc933Frontier)
	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	acc933Write(t, file, acc933Frontier+"    now()\n")
	spyOn(t, client).failPin = true

	err := client.ResumeOnThisFile(testItem)

	require.ErrorIs(t, err, errSpy)
	assert.True(t, run{Status: exactRun(t, client)}.terminal(), "the canceled run stays canceled")
	requireMoveLockFree(t, client)
}

func TestEngine_ARunStartedOutsideTheStoreIsRefusedBeforeItOpensAFile(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	_, err := engine.runStar(engine.ctx, ItemInput{Item: testItem}, "", "")

	require.ErrorIs(t, err, errNoRow)
}

func TestClient_ResumeOnThisFileThatCannotReadTheRunsBelowDoesNotCancelTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, acc933Frontier)
	acc933Write(t, file, acc933Frontier+"    now()\n")
	client := h.client(t, testVersion)
	last, err := client.newest(testItem)
	require.NoError(t, err)

	spyOn(t, client).listFailAt = 1

	err = client.setPin(last, []workflow.PinnedFile{{Path: "workflow.star", SHA256: "x"}})

	require.ErrorIs(t, err, errSpy)
}

func TestClient_ResumeOnThisFileRefusesARunWhoseStoredPinIsNoPinAndMovesNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, acc933Frontier)
	acc933Write(t, file, acc933Frontier+"    now()\n")
	client := h.client(t, testVersion)
	require.NoError(t, dbos.SetWorkflowAttributes(client.flow, testRun, map[string]any{pinAttribute: 7}, dbos.WithMergeAttributes()))

	err := client.ResumeOnThisFile(testItem)

	require.ErrorContains(t, err, "the pin of run "+testRun)
	assert.False(t, run{Status: exactRun(t, client)}.terminal(), "the run is not canceled")
}

func TestClient_ResumeOnThisFileRefusesARunWhoseInputCannotBeRead(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)

	_, err := client.pinToMove(testItem, run{Status: dbos.WorkflowStatus{ID: testRun, Input: "not json"}}, true)

	require.ErrorContains(t, err, "read the input of run "+testRun)
}

func TestEngine_PinInForceNamesTheRunWhoseRowIsGoneAndTheReadThatFailed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	_, err := engine.pinInForce(engine.ctx, ItemInput{})
	require.ErrorIs(t, err, errNoRow, "a context that runs no workflow has no row")

	require.NoError(t, engine.Shutdown())

	_, err = engine.pinInForce(engine.ctx, ItemInput{})
	require.Error(t, err)
	require.ErrorContains(t, err, "read the pin of run")
}

func TestClient_SetPinReachesTheItemRunsBelowAndNoPhase(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := writeFlow(t, "def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\")\n    gather(spawn(items = [\"21\"], entry = \"worker\", name = \"fan\"), name = \"fan\")\n\ndef worker(item):\n    now()\n")
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "work.md"), []byte("Work.\n"), 0o600))

	_, err := runFileWithin(t, h.engine(t), file)
	require.NoError(t, err)

	client := h.client(t, testVersion)
	last, err := client.newest(testItem)
	require.NoError(t, err)

	pin := []workflow.PinnedFile{{Path: "workflow.star", SHA256: "adopted"}}
	require.NoError(t, client.setPin(last, pin))

	rows, err := client.rowsUnder(testRun)
	require.NoError(t, err)

	names := map[string]bool{}

	for _, row := range rows {
		names[row.Name] = true
		_, has := row.Attributes[pinAttribute]
		assert.Equal(t, row.Name == itemName, has, "%s (%s): only an item run carries the pin", row.ID, row.Name)
	}

	assert.True(t, names[itemName] && names[phaseName], "the tree holds an item run and a phase: %v", names)

	got, found := pinAttributeOf(t, client, testRun)
	require.True(t, found)
	assert.Equal(t, pin, got)
}

func TestClient_ResumeOnThisBuildAndFileMovesTheRunOfAnotherBuildOntoThisFile(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	harness.flowIs(t, waitFlow+"\n# edited\n")
	expireLease(harness)

	require.NoError(t, other.ResumeOnThisBuildAndFile(testItem))

	row, err := other.row(testRun)
	require.NoError(t, err)
	assert.Equal(t, other.version, row.ApplicationVersion, "one move did both: the run is on this build")

	pin, found := pinAttributeOf(t, other, testRun)
	require.True(t, found)

	want, err := workflow.Pin(filepath.Join(filepath.Dir(harness.opts.StateDir), defaultFlowName))
	require.NoError(t, err)
	assert.Equal(t, want, pin, "the pin is the file as it is on disk now")
}

func TestClient_ResumeRunWithTheFileFlagAdoptsTheFileAndReturnsTheIdOfTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, acc933Frontier)
	acc933Write(t, file, acc933Frontier+"    now()\n")

	client := h.client(t, testVersion)
	id, err := client.ResumeRun(testItem, false, true)
	require.NoError(t, err)

	want, err := workflow.Pin(file)
	require.NoError(t, err)

	pin, found := pinAttributeOf(t, client, testRun)
	require.True(t, found, "the resume wrote the pin on the run")
	assert.Equal(t, want, pin, "the pin is the file as it is on disk now")
	assert.Equal(t, testRun, id)
}

func TestClient_ResumeRunWithBothFlagsMovesTheRunOntoThisBuildAndFileAndReturnsItsId(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	harness.flowIs(t, waitFlow+"\n# edited\n")
	expireLease(harness)

	id, err := other.ResumeRun(testItem, true, true)
	require.NoError(t, err)

	row, err := other.row(testRun)
	require.NoError(t, err)
	assert.Equal(t, other.version, row.ApplicationVersion, "the run is on this build")

	pin, found := pinAttributeOf(t, other, testRun)
	require.True(t, found)

	want, err := workflow.Pin(filepath.Join(filepath.Dir(harness.opts.StateDir), defaultFlowName))
	require.NoError(t, err)
	assert.Equal(t, want, pin, "the pin is the file as it is on disk now")
	assert.Equal(t, testRun, id)
}

func TestClient_AChangedFileRefusesAMoveToThisBuildAndNamesBothFlags(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	harness.flowIs(t, waitFlow+"\n# edited\n")

	err := other.ResumeOnThisBuild(testItem)

	require.ErrorContains(t, err, "changed since the run started")
	require.ErrorContains(t, err, "beehave resume "+testItem+" --on-this-build --on-this-file")
}

func TestPinOf_TheAttributeWinsOverTheInputAndAMissingOrClearedOneReadsTheInput(t *testing.T) {
	t.Parallel()

	input := ItemInput{Files: []workflow.PinnedFile{{Path: "workflow.star", SHA256: "old"}}}
	adopted := []workflow.PinnedFile{{Path: "workflow.star", SHA256: "new"}, {Path: "lib.star", SHA256: "lib"}}

	tests := map[string]struct {
		attributes map[string]any
		want       []workflow.PinnedFile
	}{
		"no attribute":      {nil, input.Files},
		"another attribute": {map[string]any{moveAttribute: "x"}, input.Files},
		"cleared":           {map[string]any{pinAttribute: nil}, input.Files},
		"set":               {map[string]any{pinAttribute: adopted}, adopted},
	}

	for name, tt := range tests {
		got, err := pinOf(dbos.WorkflowStatus{Attributes: tt.attributes}, input)

		require.NoError(t, err, name)
		assert.Equal(t, tt.want, got, name)
	}
}

func TestPinOf_AnAttributeThatIsNoPinIsAnErrorAndNeverTheInput(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]any{"a number": 7, "a value that is no json": make(chan int)} {
		_, err := pinOf(dbos.WorkflowStatus{ID: "item-12-r1", Attributes: map[string]any{pinAttribute: raw}}, ItemInput{})

		require.Error(t, err, name)
		require.ErrorContains(t, err, "the pin of run item-12-r1", name)
	}
}

func TestReplayMismatch_OnlyTheTwoStepMismatchCodesOfTheStoreCount(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err  error
		want bool
	}{
		"a step recorded under another name": {&dbos.Error{Code: dbos.ErrorCodeUnexpectedStep}, true},
		"a step recorded twice":              {&dbos.Error{Code: dbos.ErrorCodeStepNondeterminism}, true},
		"the same error, wrapped":            {wrapped(&dbos.Error{Code: dbos.ErrorCodeUnexpectedStep}), true},
		"another code of the store":          {&dbos.Error{Code: dbos.ErrorCodeTimeout}, false},
		"an error of no engine":              {errFake, false},
		"no error":                           {nil, false},
	}

	for name, tt := range tests {
		assert.Equal(t, tt.want, replayMismatch(tt.err), name)
	}
}

// wrapped wraps err the way the builtins wrap the error of a step.
func wrapped(err error) error {
	return fmt.Errorf("step failed: %w", err)
}

const guardFlow = "def main(item):\n    write(run = [\"make\", \"a\"], undo = [\"drop\", \"a\"], name = \"a\")\n    wait(name = \"ask\", question = \"What now?\")\n\ndef finish(item):\n    command(name = \"cleanup\", run = [\"cleanup\"])\n"

func TestAdopt_AChangeBeforeTheReachedStepFailsTheRunAndUndoesNoRecordedWrite(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, guardFlow)
	require.Equal(t, []string{"make a"}, h.commands.ran(), "the write is recorded before the change")

	acc933Write(t, file, strings.Replace(guardFlow, "def main(item):\n", "def main(item):\n    now()\n", 1))

	client := h.client(t, testVersion)
	require.NoError(t, client.ResumeOnThisFile(testItem))

	engine := h.engine(t)
	last := acc933Last(t, engine, func(r run) bool { return r.terminal() })

	require.Equal(t, runFailed, stateOf(last.Status.Status), acc933Err(last))
	require.ErrorContains(t, last.Status.Error, "was recorded when", "the failure names the step that differs")
	assert.NotContains(t, acc933Err(last), "undone", "the failure does not claim an undo")
	assert.NotContains(t, acc933Err(last), "finish:", "the finish function did not run on a history that does not fit the file")
	assert.Equal(t, 1, strings.Count(acc933Err(last), "was recorded when"), "the failure is the one mismatch, with no second error behind it")
	assert.Equal(t, []string{"make a"}, h.commands.ran(), "no undo of the recorded write ran, and neither did the finish function or a new step")

	steps, err := dbos.GetWorkflowSteps(engine.ctx, last.Status.ID)
	require.NoError(t, err)

	for _, step := range steps {
		assert.NotContains(t, []string{string(model.StepRelease), treeStepName}, step.StepName, "the failed replay recorded no release step, which would stand in the way of the next replay")
	}

	h.life.mu.Lock()
	defer h.life.mu.Unlock()
	assert.Contains(t, h.life.released, "owner-12#2", "the claim that the adoption took went back, so a second adoption can take it")
}

const childFlow = "def main(item):\n    now()\n    wait(name = \"ask\", question = \"What now?\")\n    gather(spawn(items = [\"21\"], entry = \"worker\", name = \"fan\"), name = \"fan\")\n\ndef worker(item):\n    now()\n"

func TestAdopt_AChildOfAnAdoptedRunReplaysTheNewPin(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := onThisFileRun(t, h, childFlow)
	acc933Write(t, file, childFlow+"\n# edited\n")

	client := h.client(t, testVersion)
	require.NoError(t, client.ResumeOnThisFile(testItem))

	engine := h.engine(t)
	last := acc933Last(t, engine, func(r run) bool { acc933Answer(client); return r.terminal() })
	require.Equal(t, runDone, stateOf(last.Status.Status), acc933Err(last))

	child, found, err := latestRun(engine.store(), "21")
	require.NoError(t, err)
	require.True(t, found, "the adopted run started its child")

	assert.Equal(t, runDone, stateOf(child.Status.Status), acc933Err(child))
	assert.Equal(t, acc933Hash(t, file), acc933PinHash(child), "the child holds the pin of the adopted run, not the pin of the input")
}
