package flow

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

// stepsFlow is an agent call that declares a verdict, then a gate, then a command.
const stepsFlow = "def main(item):\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", outputs = [\"verdict\"], name = \"a\")\n" +
	"    gate(name = \"g\")\n" +
	"    command(run = [\"lint\"], name = \"c\")\n"

// safeBuffer is a buffer that the run goroutines and the test read and write at once.
type safeBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.text.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.text.String()
}

// viewNames returns the name of each line of the view.
func viewNames(view RunSteps) []string {
	names := make([]string, 0, len(view.Steps))
	for _, line := range view.Steps {
		names = append(names, line.Name)
	}

	return names
}

func TestClient_StepsOfAFinishedRunShowEachStepWithTheFieldsThatDecideTheFlow(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"approve"}`, exited)
	rig.h.commands.reply = lintRCs(3)

	_, err := rig.run(t, stepsFlow, "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	view, err := rig.h.client(t, testVersion).Steps(testRun)
	require.NoError(t, err)

	assert.Equal(t, testRun, view.Run)
	assert.Equal(t, runDone, view.State)
	assert.Equal(t, []string{"claim", "item", "login#1", "agent:a#1", "gate:g#1", "command:c#1", "worktree", "release"}, viewNames(view))

	for _, line := range view.Steps {
		assert.Equal(t, "done", line.State, line.Name)
		assert.False(t, line.Started.IsZero(), line.Name)
		assert.False(t, line.Ended.IsZero(), line.Name)
		assert.GreaterOrEqual(t, line.Seconds, 0.0, line.Name)
	}

	work := view.Steps[3]
	require.NotNil(t, work.ExitCode)
	assert.Equal(t, 0, *work.ExitCode)
	assert.Equal(t, "approve", work.Verdict)
	assert.Equal(t, rig.head, work.Head)
	assert.Empty(t, work.Stop)

	gate := view.Steps[4]
	require.NotNil(t, gate.Passed)
	assert.True(t, *gate.Passed)
	assert.Equal(t, []GateStep{{Name: "command", Status: "pass"}}, gate.Gate)

	lint := view.Steps[5]
	require.NotNil(t, lint.ExitCode)
	assert.Equal(t, 3, *lint.ExitCode)
}

func TestClient_StepsOfARunInItsAgentStepShowTheStepAsRunningWithItsElapsedTime(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	release := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(release)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })

	view, err := client.Steps(testRun)
	require.NoError(t, err)

	assert.Equal(t, runRunning, view.State)
	require.NotEmpty(t, view.Steps)

	last := view.Steps[len(view.Steps)-1]
	assert.Equal(t, "agent:work#1", last.Name)
	assert.Equal(t, "running", last.State)
	assert.True(t, last.Ended.IsZero())
	assert.False(t, last.Started.IsZero())
	assert.GreaterOrEqual(t, last.Seconds, 0.0)

	for _, line := range view.Steps[:len(view.Steps)-1] {
		assert.Equal(t, "done", line.State, line.Name)
	}

	release()
	require.NoError(t, (<-done).err)

	view, err = client.Steps(testRun)
	require.NoError(t, err)
	assert.Equal(t, runDone, view.State)
	assert.Equal(t, "done", view.Steps[len(view.Steps)-3].State, "the agent step ended")
}

func TestClient_StepsOfARunWithAFailedStepNameTheStepAndItsError(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{}, errors.New("the lint program is missing")
	}
	harness.flowIs(t, bodyFlow("command(run = [\"lint\"], name = \"c\")\n"))
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	view, err := harness.client(t, testVersion).Steps(testRun)
	require.NoError(t, err)

	assert.Equal(t, runFailed, view.State)

	var failed []StepLine

	for _, line := range view.Steps {
		if line.State == "failed" {
			failed = append(failed, line)
		}
	}

	require.Len(t, failed, 1)
	assert.Equal(t, "command:c#1", failed[0].Name)
	assert.Contains(t, failed[0].Error, "the lint program is missing")
}

func TestClient_StepsOfARunThatFailedOutsideAStepCarryTheErrorOfTheRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, bodyFlow("fail(\"boom\")\n"))
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	view, err := harness.client(t, testVersion).Steps(testRun)
	require.NoError(t, err)

	assert.Equal(t, runFailed, view.State)
	assert.Contains(t, view.Error, "boom")

	for _, line := range view.Steps {
		assert.NotEqual(t, stepFailed, line.State, "no step failed: the cause is the run error")
	}
}

func TestClient_StepsOfAFinishedRunCarryNoRunError(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, bodyFlow("command(run = [\"lint\"], name = \"c\")\n"))
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	view, err := harness.client(t, testVersion).Steps(testRun)
	require.NoError(t, err)

	assert.Empty(t, view.Error)
}

func TestClient_StepsOfAnAgentThatCappedShowTheStopReason(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", agent.Result{Outcome: agent.OutcomeTurnCap, Session: "sess-1"})

	_, err := rig.run(t, bodyFlow(agentCallText("")), "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	view, err := rig.h.client(t, testVersion).Steps(testRun)
	require.NoError(t, err)

	var capped []StepLine

	for _, line := range view.Steps {
		if line.Stop != "" {
			capped = append(capped, line)
		}
	}

	require.Len(t, capped, 1)
	assert.Equal(t, "agent:a#1", capped[0].Name)
	assert.Equal(t, "turns", capped[0].Stop)
}

func TestClient_StepsRefusesARunThatIsNotInTheStore(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)

	_, err := client.Steps("item-99-r1")

	require.ErrorIs(t, err, errNoSuchRun)
	require.ErrorContains(t, err, "item-99-r1")
}

func TestClient_StepsShowAStopOfACommandThatStoppedTheItem(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.commands.reply = lintRCs(4)
	harness.flowIs(t, bodyFlow("command(run = [\"lint\"], stop_on_rc = [4], name = \"c\")\n"))
	engine := harness.engine(t)

	result, err := engine.Run(testItem, testProject)
	require.NoError(t, err)
	require.NotNil(t, result.Stopped)

	view, err := harness.client(t, testVersion).Steps(testRun)
	require.NoError(t, err)

	var stopped *StepLine

	for index, line := range view.Steps {
		if line.Name == "command:c#1" {
			stopped = &view.Steps[index]
		}
	}

	require.NotNil(t, stopped)
	assert.Equal(t, "stop_on_rc", stopped.Stop)
	require.NotNil(t, stopped.ExitCode)
	assert.Equal(t, 4, *stopped.ExitCode)
}

func TestEngine_ProgressPrintsTheRunAndEachTopLevelStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"approve"}`, exited)

	var out safeBuffer

	rig.h.opts.Progress = &out

	file := writeFlow(t, stepsFlow)
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("Write to {{ .output_file }}\n"), 0o600))

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	text := out.String()
	assert.Contains(t, text, "run "+testRun+" workflow "+file+" item "+testItem+"\n")

	for _, want := range []string{
		testRun + " step agent:a started\n", testRun + " step agent:a done in ",
		testRun + " step gate:g started\n", testRun + " step gate:g done in ",
		testRun + " step command:c started\n", testRun + " step command:c done in ",
	} {
		assert.Contains(t, text, want)
	}
}

func TestEngine_ProgressNamesAStepThatFailed(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{}, errors.New("the lint program\n  is missing")
	}
	harness.flowIs(t, bodyFlow("command(run = [\"lint\"], name = \"c\")\n"))

	var out safeBuffer

	harness.opts.Progress = &out

	_, err := harness.engine(t).Run(testItem, testProject)
	require.Error(t, err)

	assert.Contains(t, out.String(), testRun+" step command:c failed after ")
	assert.Contains(t, out.String(), "the lint program is missing")
}

func TestClient_StepsOfAStoreThatIsShutDownRefusesTheRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)

	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{Lifecycle: harness.life, Trees: harness.trees, Clock: clock{Release: time.Second}})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, err = client.Steps(testRun)

	require.ErrorContains(t, err, "read the run "+testRun)
}

func TestClient_StepsOfARunWhoseStepRecordsCannotBeReadReportsTheRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"approve"}`, exited)

	_, err := rig.run(t, stepsFlow, "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	client := rig.h.client(t, testVersion)

	db, err := sql.Open("sqlite", filepath.Join(rig.h.opts.StateDir, databaseFile)+"?_pragma=busy_timeout(30000)")
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	// A step number that is text cannot be read as a number.
	_, err = db.ExecContext(t.Context(), "UPDATE operation_outputs SET function_id = 'broken' WHERE workflow_uuid = ? AND function_id = (SELECT MAX(function_id) FROM operation_outputs WHERE workflow_uuid = ?)", testRun, testRun)
	require.NoError(t, err)

	_, err = client.Steps(testRun)

	require.ErrorContains(t, err, "read the steps of "+testRun)
}

func TestClient_StepsOfARunWhoseChildWorkflowsCannotBeReadReportsTheRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"approve"}`, exited)

	_, err := rig.run(t, stepsFlow, "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	client := rig.h.client(t, testVersion)

	db, err := sql.Open("sqlite", filepath.Join(rig.h.opts.StateDir, databaseFile)+"?_pragma=busy_timeout(30000)")
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	// A creation time that is text cannot be read as a number. The run row stays readable.
	_, err = db.ExecContext(t.Context(), "UPDATE workflow_status SET created_at = 'broken' WHERE workflow_uuid <> ?", testRun)
	require.NoError(t, err)

	_, err = client.Steps(testRun)

	require.ErrorContains(t, err, "read the child workflows of a run")
}

func TestStepLine_TheOutputsTheEngineRecordsDecodeIntoTheFieldsOfTheLine(t *testing.T) {
	t.Parallel()

	record := func(name string, output any) dbos.StepInfo {
		data, err := json.Marshal(output)
		require.NoError(t, err)

		return dbos.StepInfo{StepName: name, Output: string(data)}
	}

	gate := callLine([]dbos.StepInfo{record("gate:g#1", stepResult{Outputs: gateOutputs(false, "h1", "t1", "r", []GateStep{{Name: "lint", Status: "fail"}}, nil)})}, "gate:g#1")
	require.NotNil(t, gate.Passed)
	assert.False(t, *gate.Passed)
	assert.Equal(t, []GateStep{{Name: "lint", Status: "fail"}}, gate.Gate)
	assert.Equal(t, "h1", gate.Head)

	command := callLine([]dbos.StepInfo{record("command:c#1", stepResult{Outputs: commandOutputs("o", "e", 7)})}, "command:c#1")
	require.NotNil(t, command.ExitCode)
	assert.Equal(t, 7, *command.ExitCode)

	stopped := callLine([]dbos.StepInfo{record("command:c#2", stepResult{Outputs: commandOutputs("", "", 4), Stopped: &Stopped{Step: "c", RC: 4}})}, "command:c#2")
	assert.Equal(t, stopOnRC, stopped.Stop)

	phase := callLine([]dbos.StepInfo{withChild(record("any-name", PhaseOutput{Outcome: "exited", ExitCode: 2}))}, "agent:a#1")
	require.NotNil(t, phase.ExitCode)
	assert.Equal(t, 2, *phase.ExitCode)

	noOutcome := callLine([]dbos.StepInfo{withChild(record("gate:stop", map[string]any{"holder": "x"}))}, "gate:g#1")
	assert.Nil(t, noOutcome.ExitCode, "the child workflow of a gate has no outcome, so the gate line has no exit code")
	assert.False(t, noOutcome.OutputUnreadable)

	read := callLine([]dbos.StepInfo{record("agent:a#1/read1", stepResult{Outputs: withVerdict(agentOutputs("t", "s", "p", "h2", "", "turns", 0.5, true), "approve")})}, "agent:a#1/read1")
	assert.Equal(t, "h2", read.Head)
	assert.Equal(t, "turns", read.Stop)
	assert.Equal(t, "approve", read.Verdict)
	assert.True(t, read.Rewrote)

	other := callLine([]dbos.StepInfo{record("scope:s#1", stepResult{Outputs: map[string]any{"rc": 9, "head": "x"}})}, "scope:s#1")
	assert.Nil(t, other.ExitCode, "a step of another kind has no exit code")
	assert.Empty(t, other.Head)
}

// withChild marks a record as the call of a child workflow, whose output the record holds in this test.
func withChild(record dbos.StepInfo) dbos.StepInfo {
	record.ChildWorkflowID = "child-1"

	return record
}

func TestStepLine_AnOutputThatDoesNotDecodeKeepsTheStepAndSaysTheOutputCannotBeRead(t *testing.T) {
	t.Parallel()

	for name, record := range map[string]dbos.StepInfo{
		"command wrong shape": {StepName: "command:c#1", Output: `{"outputs":"text"}`},
		"command not json":    {StepName: "command:c#1", Output: `not json`},
		"gate wrong shape":    {StepName: "gate:g#1", Output: `{"outputs":[1]}`},
		"read wrong shape":    {StepName: "agent:a#1/read1", Output: `{"outputs":5}`},
		"phase wrong shape":   {StepName: "phase", ChildWorkflowID: "child-1", Output: `[1]`},
		"output not text":     {StepName: "command:c#1", Output: 42},
	} {
		line := callLine([]dbos.StepInfo{record}, record.StepName)

		assert.Equal(t, stepDone, line.State, name)
		assert.True(t, line.OutputUnreadable, name)
		assert.Nil(t, line.ExitCode, name)
	}

	readable := callLine([]dbos.StepInfo{{StepName: "scope:s#1", Output: `anything`}}, "scope:s#1")
	assert.False(t, readable.OutputUnreadable, "a step of a kind the view does not decode has nothing to read")

	data, err := json.Marshal(callLine([]dbos.StepInfo{{StepName: "command:c#1", Output: `x`}}, "command:c#1"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"output_unreadable":true`)
}

func TestClient_StepsReadTheExitCodeOfAnAgentFromItsChildWorkflowNotFromTheLibraryRecord(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"approve"}`, exited)

	_, err := rig.run(t, stepsFlow, "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	client := rig.h.client(t, testVersion)

	db, err := sql.Open("sqlite", filepath.Join(rig.h.opts.StateDir, databaseFile)+"?_pragma=busy_timeout(30000)")
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	// Empty the output of the record that the library keeps for the result of a child workflow. The child id of the record and the output of the child remain.
	_, err = db.ExecContext(t.Context(), "UPDATE operation_outputs SET output = NULL WHERE workflow_uuid = ? AND function_name = 'DBOS.getResult'", testRun)
	require.NoError(t, err)

	view, err := client.Steps(testRun)
	require.NoError(t, err)

	work := view.Steps[3]
	require.NotNil(t, work.ExitCode, "the exit code comes from the output of the child workflow")
	assert.Equal(t, 0, *work.ExitCode)
	assert.False(t, work.OutputUnreadable)
	assert.Nil(t, view.Steps[4].ExitCode, "the gate line has no exit code")
}

func withVerdict(outputs map[string]any, verdict string) map[string]any {
	outputs[outputVerdict] = verdict

	return outputs
}

func TestStepLines_AnOpenRunThatRecordedNoStepForItsWorkShowsAStepInProgress(t *testing.T) {
	t.Parallel()

	ended := time.Now().Add(-time.Minute)
	records := []dbos.StepInfo{{StepName: "command:c#1", StartedAt: ended.Add(-time.Second), CompletedAt: ended}}

	lines := stepLines(records, runRunning, true, ended.Add(time.Minute))

	require.Len(t, lines, 2)
	assert.Equal(t, unrecordedStep, lines[1].Name)
	assert.Equal(t, "running", lines[1].State)
	assert.Equal(t, ended, lines[1].Started)
	assert.InDelta(t, 60.0, lines[1].Seconds, 0.001)
	assert.Len(t, stepLines(records, runDone, false, ended), 1, "a finished run shows no step in progress")
	assert.Empty(t, stepLines(nil, runRunning, true, ended), "a run with no record has no start for a step")
}

func TestStepLines_AWaitingGateIsNamedFromItsTipRecordAndAnOtherRecordFromItself(t *testing.T) {
	t.Parallel()

	now := time.Now()

	waiting := stepLines([]dbos.StepInfo{{StepName: gateTipName, StartedAt: now}}, runRunning, true, now)
	require.Len(t, waiting, 1)
	assert.Equal(t, kindGate, waiting[0].Name)

	other := stepLines([]dbos.StepInfo{{StepName: "DBOS.sleep", StartedAt: now}}, runRunning, true, now)
	require.Len(t, other, 1)
	assert.Equal(t, "DBOS.sleep", other[0].Name)
}

func TestStepLine_AnAgentLineNamesTheKindOfItsPhase(t *testing.T) {
	t.Parallel()

	record := func(name string, output any) dbos.StepInfo {
		data, err := json.Marshal(output)
		require.NoError(t, err)

		return dbos.StepInfo{StepName: name, Output: string(data)}
	}

	review := callLine([]dbos.StepInfo{withChild(record("any-name", PhaseOutput{Outcome: "exited", Kind: "review"}))}, "agent:a#1")
	assert.Equal(t, "review", review.Kind)

	rework := callLine([]dbos.StepInfo{withChild(record("any-name", PhaseOutput{Outcome: "exited", Kind: "rework"}))}, "agent:a#1")
	assert.Equal(t, "rework", rework.Kind)

	gate := callLine([]dbos.StepInfo{withChild(record("gate:stop", map[string]any{"holder": "x"}))}, "gate:g#1")
	assert.Empty(t, gate.Kind, "a gate is not a lane, so its line has no kind")
}
