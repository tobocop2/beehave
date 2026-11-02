package flow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/obee/internal/review"
	trackerstore "github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/workflow"
)

// fakeCommands records each request. reply decides the output; it runs under the lock, so it needs no lock of its own.
type fakeCommands struct {
	mu       sync.Mutex
	requests []CommandRequest
	reply    func(CommandRequest) (CommandOutput, error)
	// before runs ahead of the lock, so it may wait for the context or for a signal of the test. A non-nil error fails the command.
	before func(ctx context.Context, request CommandRequest) error
}

func (c *fakeCommands) Run(ctx context.Context, request CommandRequest) (CommandOutput, error) {
	if c.before != nil {
		if err := c.before(ctx, request); err != nil {
			return CommandOutput{}, err
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.requests = append(c.requests, request)

	if c.reply == nil {
		return CommandOutput{}, nil
	}

	return c.reply(request)
}

// ran returns the program of each request, words joined by a space.
func (c *fakeCommands) ran() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []string
	for _, request := range c.requests {
		out = append(out, strings.Join(request.Run, " "))
	}

	return out
}

type fakeItems struct {
	fields map[string]string
	err    error
}

func (i *fakeItems) Fields(context.Context, string, string) (map[string]string, error) {
	return i.fields, i.err
}

// lintRCs answers each lint call with the next code. The last code repeats.
func lintRCs(codes ...int) func(CommandRequest) (CommandOutput, error) {
	calls := 0

	return func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] != "lint" {
			return CommandOutput{}, nil
		}

		code := codes[min(calls, len(codes)-1)]
		calls++

		return CommandOutput{RC: code}, nil
	}
}

// writeFlow writes a workflow file in a new folder and returns its path.
func writeFlow(t *testing.T, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "workflow.star")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

// bodyFlow wraps the statements in the main function of a workflow file, so a test writes only the steps.
func bodyFlow(body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")

	return "def main(item):\n    " + strings.Join(lines, "\n    ") + "\n"
}

// walkBody runs the statements as the main function of a workflow file for the test item.
func walkBody(t *testing.T, h *harness, body string) (ItemResult, error) {
	t.Helper()

	return walkText(t, h, bodyFlow(body))
}

// walkText runs the workflow text for the test item.
func walkText(t *testing.T, h *harness, text string) (ItemResult, error) {
	t.Helper()

	return h.engine(t).RunFile(testItem, testProject, writeFlow(t, text))
}

// stepNames returns the names of the builtin steps of the run, which are the ones with a count.
func stepNames(t *testing.T, engine *Engine) []string {
	t.Helper()

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	var names []string

	for _, step := range steps {
		if strings.Contains(step.StepName, "#") {
			names = append(names, step.StepName)
		}
	}

	return names
}

// interrupted runs the file until it waits for the owner, then stops the engine as a crash would.
func interrupted(t *testing.T, h *harness, file string) {
	t.Helper()

	stopCtx, stop := context.WithCancel(t.Context())
	engine, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	client := h.client(t, testVersion)

	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file)
		ended <- runErr
	}()

	eventually(t, func() bool {
		rows, err := client.Status()

		return err == nil && len(rows) == 1 && rows[0].Question != ""
	})

	stop()
	require.NoError(t, engine.Shutdown())
}

// refusalWithin is how long a refusal may take. A refusal comes before the first step, so it is quick.
const refusalWithin = 10 * time.Second

// runFileWithin runs the file and fails the test when the run does not end within refusalWithin, so a missing refusal is an assertion and not a hang.
func runFileWithin(t *testing.T, engine *Engine, file string) (ItemResult, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), refusalWithin)
	defer cancel()

	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	select {
	case outcome := <-done:
		return outcome.result, outcome.err
	case <-ctx.Done():
		t.Fatalf("the run did not end within %s: it neither finished nor refused the file", refusalWithin)
	}

	return ItemResult{}, nil
}

const crashFlow = `def main(item):
    command(name = "before", run = ["before"])
    answer = wait(name = "ask", question = "go?").answer
    command(name = "after", run = ["after", answer])
`

func TestRunStar_AReplayIssuesTheSameStepNamesAndRunsNoRecordedStepAgain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := writeFlow(t, crashFlow)
	interrupted(t, h, file)

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "yes") == nil })

	outcome := <-done

	require.NoError(t, outcome.err)
	assert.Equal(t, []string{"before", "after yes"}, h.commands.ran(), "the command before the wait ran once")
	assert.Equal(t, []string{"command:before#1", "wait:ask#1", "command:after#2"}, stepNames(t, next))
}

// goldenFlow calls every builtin that records a step. Its step names are the contract that stepsVersion guards.
const goldenFlow = `def main(item):
    env("HOME")
    command(name = "c", run = ["c"])
    checked = gate(name = "g")
    agent(name = "a", prompt = "prompts/work.md", model = "m")
    wait(name = "w", question = "go?")
    merge(name = "m", head = checked.head)
`

// goldenSteps are the step names that goldenFlow records under stepsVersion star13.
// A change to this list is a change to the sequence of a builtin: bump stepsVersion in the same commit.
var goldenSteps = []string{
	"env:HOME#1", "command:c#1", "scope:g#1", "gate:g#1",
	"login#1", "agent:a#1/render", "agent:a#1/clear1", "agent:a#1/read1",
	"wait:w#1", "merge:m#1",
}

func TestRunStar_TheStepNamesOfEveryBuiltinMatchTheGoldenListOfTheStepsVersion(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"HOME": "/home/x"}
	h.flowIs(t, goldenFlow)
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead}}
	h.forge.state = review.Standing{Number: testPull, Head: gatedHead}

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitForTheOwner(t, h)
	eventually(t, func() bool { return client.Answer(testItem, "yes") == nil })

	require.NoError(t, (<-done).err)
	assert.Equal(t, "star13", stepsVersion)
	assert.Equal(t, goldenSteps, stepNames(t, engine), "the step names changed: bump stepsVersion and update the list")
}

func TestStrandedError_ARunStoredUnderTheBareVersionNamesTheOlderStepFormat(t *testing.T) {
	t.Parallel()

	stored := run{Status: dbos.WorkflowStatus{ID: "item-12-r1", ApplicationVersion: "v1"}}

	err := strandedError(stored, applicationVersion("v1"))

	require.ErrorIs(t, err, errStranded)
	assert.Contains(t, err.Error(), "older step format, stored as version v1, and this is v1+star13")
	assert.NotContains(t, err.Error(), "Start version")
}

func TestStrandedError_ASpawnedRunStoredBeforeTheHoldStepIsRefusedNotReplayed(t *testing.T) {
	t.Parallel()

	stored := run{Status: dbos.WorkflowStatus{ID: "item-12-r1", ApplicationVersion: "v1+star2"}}

	err := strandedError(stored, applicationVersion("v1"))

	require.ErrorIs(t, err, errStranded)
	assert.Contains(t, err.Error(), "stored as version v1+star2, and this is v1+star13", "a run from before the hold:start step cannot replay against the new step list")
}

func TestStrandedError_ARunStoredBeforeTheIssueReadAfterTheBatchIsRefusedNotReplayed(t *testing.T) {
	t.Parallel()

	stored := run{Status: dbos.WorkflowStatus{ID: "item-12-r1", ApplicationVersion: "v1+star5"}}

	err := strandedError(stored, applicationVersion("v1"))

	require.ErrorIs(t, err, errStranded)
	assert.Contains(t, err.Error(), "stored as version v1+star5, and this is v1+star13", "a run from before the batch:issues step cannot replay against the new step list")
}

func TestStrandedError_ARunStoredBeforeTheSendBackReadIsRefusedNotReplayed(t *testing.T) {
	t.Parallel()

	stored := run{Status: dbos.WorkflowStatus{ID: "item-12-r1", ApplicationVersion: "v1+star6"}}

	err := strandedError(stored, applicationVersion("v1"))

	require.ErrorIs(t, err, errStranded)
	assert.Contains(t, err.Error(), "stored as version v1+star6, and this is v1+star13", "a run whose send back step recorded no outcome cannot replay against the new step")
}

func TestStrandedError_ARunStoredBeforeTheStopOutputOfAnAgentCallIsRefusedNotReplayed(t *testing.T) {
	t.Parallel()

	stored := run{Status: dbos.WorkflowStatus{ID: "item-12-r1", ApplicationVersion: "v1+star7"}}

	err := strandedError(stored, applicationVersion("v1"))

	require.ErrorIs(t, err, errStranded)
	assert.Contains(t, err.Error(), "stored as version v1+star7, and this is v1+star13", "a run whose agent step recorded no stop output cannot replay against the new step")
}

func TestStrandedError_ARunStoredBeforeTheVoidMarkOfABatchGateIsRefusedNotReplayed(t *testing.T) {
	t.Parallel()

	stored := run{Status: dbos.WorkflowStatus{ID: "item-12-r1", ApplicationVersion: "v1+star8"}}

	err := strandedError(stored, applicationVersion("v1"))

	require.ErrorIs(t, err, errStranded)
	assert.Contains(t, err.Error(), "stored as version v1+star8, and this is v1+star13", "a run whose batch gate recorded no void mark cannot replay against the new gate output")
}

func TestStrandedError_ARunStoredBeforeTheRecordedHeadReadOfAGateIsRefusedNotReplayed(t *testing.T) {
	t.Parallel()

	stored := run{Status: dbos.WorkflowStatus{ID: "item-12-r1", ApplicationVersion: "v1+star9"}}

	err := strandedError(stored, applicationVersion("v1"))

	require.ErrorIs(t, err, errStranded)
	assert.Contains(t, err.Error(), "stored as version v1+star9, and this is v1+star13", "a run from before the gate:tip and gate:stop steps cannot replay against the new step list")
}

func TestStrandedError_ARunStoredBeforeTheMechanismOutputOfTheReviewIsRefusedNotReplayed(t *testing.T) {
	t.Parallel()

	stored := run{Status: dbos.WorkflowStatus{ID: "item-12-r1", ApplicationVersion: "v1+star10"}}

	err := strandedError(stored, applicationVersion("v1"))

	require.ErrorIs(t, err, errStranded)
	assert.Contains(t, err.Error(), "stored as version v1+star10, and this is v1+star13", "a review recorded before the mechanism output has no mechanism for the design rule to count")
}

func TestStrandedError_ARunStoredBeforeTheScopeStepsIsRefusedNotReplayed(t *testing.T) {
	t.Parallel()

	stored := run{Status: dbos.WorkflowStatus{ID: "item-12-r1", ApplicationVersion: "v1+star11"}}

	err := strandedError(stored, applicationVersion("v1"))

	require.ErrorIs(t, err, errStranded)
	assert.Contains(t, err.Error(), "stored as version v1+star11, and this is v1+star13", "a run from before the scope steps cannot replay against the new step list")
}

func TestRunStar_AChangedFileRefusesTheReplayWithBothHashes(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := writeFlow(t, crashFlow)
	interrupted(t, h, file)
	require.NoError(t, os.WriteFile(file, []byte(strings.Replace(crashFlow, "go?", "stop?", 1)), 0o600))

	_, err := runFileWithin(t, h.engine(t), file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "changed since the run started")
}

func TestRunStar_ALoadedFileThatChangedRefusesTheReplay(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := writeFlow(t, "load(\"//lib.star\", \"step\")\n\ndef main(item):\n    step()\n    wait(question = \"go?\")\n")
	lib := filepath.Join(filepath.Dir(file), "lib.star")
	require.NoError(t, os.WriteFile(lib, []byte("def step():\n    command(run = [\"one\"])\n"), 0o600))
	interrupted(t, h, file)
	require.NoError(t, os.WriteFile(lib, []byte("def step():\n    command(run = [\"two\"])\n"), 0o600))

	_, err := runFileWithin(t, h.engine(t), file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lib.star changed since the run started")
}

func TestRunStar_TheFieldsOfTheItemFillTheItemArgument(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "def main(item):\n    command(run = [item.id, item.title])\n")

	require.NoError(t, err)
	assert.Equal(t, []string{testItem + " a title"}, h.commands.ran())
}

func TestRunStar_AFieldThatTheItemDoesNotHaveFailsTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "def main(item):\n    command(run = [item.parent])\n")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent")
	assert.Empty(t, h.commands.ran())
}

func TestRunStar_AnErrorNamesTheFileTheLineAndTheColumn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "def main(item):\n    command(run = \"not a list\" + \"\")\n")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow.star:2:12")
	assert.Contains(t, err.Error(), "run must be a list of strings")
}

func TestRunStar_AMissingMainIsAnError(t *testing.T) {
	t.Parallel()

	_, err := walkText(t, newHarness(t), "x = 1\n")

	require.ErrorContains(t, err, "defines no function main")
}

func TestRunStar_ARunawayFileStopsAtTheStepLimit(t *testing.T) {
	t.Parallel()

	_, err := walkText(t, newHarness(t), "def main(item):\n    for i in range(100000000):\n        pass\n")

	require.ErrorContains(t, err, "too many steps")
}

func TestRunStar_ACommandWithAStopCodeEndsTheItemAsStopped(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{Stdout: "no", RC: 2}, nil }

	result, err := walkText(t, h, "def main(item):\n    command(name = \"lint\", run = [\"lint\"], stop_on_rc = [2])\n    command(run = [\"never\"])\n")

	require.NoError(t, err)
	assert.Equal(t, &Stopped{Step: "command:lint#1", RC: 2, Output: "no"}, result.Stopped)
	assert.Equal(t, []string{"lint"}, h.commands.ran())
}

func TestPredeclared_NamesTheSameBuiltinsAsTheWorkflowPackage(t *testing.T) {
	t.Parallel()

	var names []string
	for name := range (&script{}).predeclared() {
		names = append(names, name)
	}

	assert.ElementsMatch(t, workflow.Builtins(), names)
}

func TestPredeclared_EveryBuiltinReadsItsKeywordsFromTheSignature(t *testing.T) {
	t.Parallel()

	table := (&script{e: &Engine{}}).predeclared()

	for _, name := range workflow.Signed() {
		fn, ok := table[name].(*starlark.Builtin)
		require.True(t, ok, name)

		_, err := fn.CallInternal(&starlark.Thread{}, nil, []starlark.Tuple{{starlark.String("zz_unknown"), starlark.MakeInt(1)}})

		require.Error(t, err, name)
		assert.NotContains(t, err.Error(), "wrong number of destinations", name)
	}
}

func TestSignature_ClosedSetsAreTheValuesThatTheRunAccepts(t *testing.T) {
	t.Parallel()

	for _, status := range workflow.StatusValues() {
		_, err := trackerstore.ParseStatus(status)
		require.NoError(t, err, status)
	}

	_, err := trackerstore.ParseStatus("nope")
	require.Error(t, err)

	for _, children := range workflow.ChildrenValues() {
		_, err := retarget(children)
		require.NoError(t, err, children)
	}

	_, err = retarget("nope")
	require.Error(t, err)

	for _, mode := range workflow.FailModes() {
		assert.Contains(t, []string{failReturn, "raise"}, mode)
	}
}

func TestToStarlark_AGoIntAndAJSONFloatGiveTheSameStarlarkInt(t *testing.T) {
	t.Parallel()

	fromInt, err := toStarlark(3)
	require.NoError(t, err)

	fromFloat, err := toStarlark(float64(3))
	require.NoError(t, err)

	assert.Equal(t, fromInt, fromFloat)
	assert.IsType(t, starlark.Int{}, fromInt)
}

func TestToStarlark_RefusesATypeAFileCannotHold(t *testing.T) {
	t.Parallel()

	_, err := toStarlark(func() {})

	require.ErrorContains(t, err, "a workflow file cannot hold this value: json: unsupported type: func()")
}

func TestToStarlark_KeepsIntFloatKeyOrderNestingAndNone(t *testing.T) {
	t.Parallel()

	got, err := toStarlark(map[string]any{
		"z": 1, "a": []any{float64(2), 2.5, nil, true, map[string]any{"y": "t", "b": int64(7)}}, "m": float64(3),
	})
	require.NoError(t, err)

	dict, ok := got.(*starlark.Dict)
	require.True(t, ok)

	want := `{"a": [2, 2.5, None, True, {"b": 7, "y": "t"}], "m": 3, "z": 1}`
	assert.Equal(t, want, dict.String(), "keys come out sorted and a whole float is an int")

	value, _, err := dict.Get(starlark.String("a"))
	require.NoError(t, err)

	list, ok := value.(*starlark.List)
	require.True(t, ok)
	assert.IsType(t, starlark.Int{}, list.Index(0))
	assert.IsType(t, starlark.Float(0), list.Index(1))
	assert.Equal(t, starlark.None, list.Index(2))
}

func TestResult_ARecordedStepStoredBeforeTheChangeStillBuildsItsStruct(t *testing.T) {
	t.Parallel()

	var done stepResult

	stored := `{"outputs":{"code":0,"ratio":0.5,"list":[1,"b",null],"nested":{"b":[{"c":true}],"a":2},"text":"x"},"note":"n"}`
	require.NoError(t, json.Unmarshal([]byte(stored), &done))

	got, err := result(done.Outputs)
	require.NoError(t, err)

	assert.Equal(t, `struct(code = 0, list = [1, "b", None], nested = {"a": 2, "b": [{"c": True}]}, ratio = 0.5, text = "x")`, got.String())
}

func TestToGo_ConvertsADictOfListsAndStructsAndRefusesAFunction(t *testing.T) {
	t.Parallel()

	thread := &starlark.Thread{}
	value, err := starlark.EvalOptions(&syntax.FileOptions{}, thread, "t", "{'a': [1, 'b', True, None, 1.5], 'c': struct(d = 'e')}", (&script{}).predeclared())
	require.NoError(t, err)

	got, err := workflow.ToGo[any](value)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"a": []any{int64(1), "b", true, nil, 1.5}, "c": map[string]any{"d": "e"}}, got)

	_, err = workflow.ToGo[any](starlark.NewBuiltin("f", nil))
	require.ErrorContains(t, err, "cannot go into a prompt")
}
