package flow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"

	"github.com/tobocop2/obee/internal/workflow"
)

// fakeCommands records each request. reply decides the output; it runs under the lock, so it needs no lock of its own.
type fakeCommands struct {
	mu       sync.Mutex
	requests []CommandRequest
	reply    func(CommandRequest) (CommandOutput, error)
}

func (c *fakeCommands) Run(_ context.Context, request CommandRequest) (CommandOutput, error) {
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

const lintAndFix = `workflow "lint-and-fix" {
  method_version = "lint-1"
}

loop "tidy" {
  max   = 3
  until = command.lint.rc == 0

  command "lint" {
    run = ["lint"]
  }

  command "fix" {
    run  = ["fix"]
    when = command.lint.rc != 0
  }
}

command "report" {
  run  = ["report"]
  when = loop.tidy.exhausted
}
`

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

func writeFlow(t *testing.T, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "workflow.hcl")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func walkText(t *testing.T, h *harness, text string) (ItemResult, error) {
	t.Helper()

	return h.engine(t).RunFile(testItem, testProject, writeFlow(t, text))
}

// stepNames returns the names of the walker steps of the run, which are the ones with a round.
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

func TestWalk_ALoopEndsOnUntilRightAfterTheLastStepItNames(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = lintRCs(1, 1, 0)
	engine := h.engine(t)

	result, err := engine.RunFile(testItem, testProject, writeFlow(t, lintAndFix))

	require.NoError(t, err)
	assert.Equal(t, ItemResult{Item: testItem}, result)
	assert.Equal(t, []string{"lint", "fix", "lint", "fix", "lint"}, h.commands.ran(), "the fix does not run after the lint that passed, and the report is skipped")
	assert.Equal(t, []string{"lint#1", "fix#1", "lint#2", "fix#2", "lint#3", "report#1"}, stepNames(t, engine), "the until check stops the round before the fix step")
}

func TestWalk_ALoopThatNeverPassesIsExhaustedAfterMaxRounds(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = lintRCs(1)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, lintAndFix))

	require.NoError(t, err)
	assert.Equal(t, []string{"lint", "fix", "lint", "fix", "lint", "fix", "report"}, h.commands.ran())
	assert.Equal(t, []string{"lint#1", "fix#1", "lint#2", "fix#2", "lint#3", "fix#3", "report#1"}, stepNames(t, engine))
}

func TestWalk_AnUntilThatNamesAStepByIndexOrNoStepChecksAfterTheLastStep(t *testing.T) {
	t.Parallel()

	for name, until := range map[string]string{
		"an_index_in_the_path":   `command["lint"].rc == 0`,
		"a_name_with_one_part":   `command.lint.rc == 0 || item == null`,
		"a_step_that_is_no_name": `item.id == "12"`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.commands.reply = lintRCs(0)
			text := strings.Replace(lintAndFix, "command.lint.rc == 0", until, 1)

			_, err := walkText(t, h, text)

			require.NoError(t, err)
			assert.Equal(t, []string{"lint"}, h.commands.ran(), "until holds after the last step, where a skipped fix does not run")
		})
	}
}

func TestWalk_AStepWhoseWhenIsFalseIsRecordedAsSkipped(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		return CommandOutput{Stdout: "out", RC: len(request.Run)}, nil
	}
	engine := h.engine(t)
	text := `workflow "w" {}
command "a" {
  run = ["a"]
}
command "b" {
  run  = ["b"]
  when = command.a.rc == 0
}
command "c" {
  run  = ["c", command.a.stdout]
  when = command.a.rc == 1
}
`

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, text))

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "c out"}, h.commands.ran())
	assert.Equal(t, []string{"a#1", "b#1", "c#1"}, stepNames(t, engine))
}

func TestWalk_ACommandWithANonZeroCodeIsDataAndAFailureToRunIsAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{}, errFake }

	_, err := walkText(t, h, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n}\n")

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "step a#1")
}

func TestWalk_ACommandPassesItsHostItsTimeoutAndTheItem(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "workflow \"w\" {}\ncommand \"a\" {\n  run     = [\"a\"]\n  host    = \"box\"\n  timeout = \"90s\"\n}\n")

	require.NoError(t, err)
	require.Len(t, h.commands.requests, 1)
	assert.Equal(t, CommandRequest{Project: testProject, Item: testItem, Run: []string{"a"}, Host: "box", Timeout: 90 * time.Second}, h.commands.requests[0])
}

func TestWalk_AnArgumentThatHasTheWrongTypeNamesTheArgument(t *testing.T) {
	t.Parallel()

	skipped := "command \"s\" {\n  run  = [\"s\"]\n  when = false\n}\n"
	tests := []struct {
		name string
		body string
		want string
	}{
		{"a_host_that_is_a_number", `host = 5`, "must be a string"},
		{"a_host_that_is_null_at_run_time", `host = command.s.stdout`, "must be a string"},
		{"a_run_that_is_a_string", `run = "ls"`, "must be a list of strings"},
		{"a_run_that_is_null_at_run_time", `run = command.s.stdout != "z" ? null : ["x"]`, "must be a list of strings"},
		{"a_timeout_that_is_no_duration", `timeout = "soon"`, `"soon" is not a duration`},
		{"a_host_with_an_unknown_name", `host = command.zzz.stdout`, "zzz"},
		{"a_run_with_an_unknown_name", `run = [command.zzz.stdout]`, "zzz"},
		{"a_timeout_with_an_unknown_name", `timeout = command.zzz.stdout`, "zzz"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			base := "run = [\"a\"]\n  "
			if strings.HasPrefix(test.body, "run") {
				base = ""
			}

			text := "workflow \"w\" {}\n" + skipped + "command \"a\" {\n  " + base + test.body + "\n}\n"

			_, err := walkText(t, h, text)

			require.ErrorContains(t, err, test.want)
			require.ErrorContains(t, err, "step a#1")
			assert.Empty(t, h.commands.ran())
		})
	}
}

func TestWalk_AWhenThatIsNotABooleanFailsTheStep(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := walkText(t, h, "workflow \"w\" {}\ncommand \"a\" {\n  run  = [\"a\"]\n  when = 5\n}\n")

	require.Error(t, err)
	assert.Empty(t, h.commands.ran())
}

func TestWalk_AGateWithNoRunIsTheProjectGate(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.gate.results = []GateResult{{Passed: true, Head: "abc", Report: "all green"}}
	text := `workflow "w" {}
gate "check" {}
command "push" {
  run  = ["push", gate.check.head, gate.check.report]
  when = gate.check.passed
}
`

	_, err := walkText(t, h, text)

	require.NoError(t, err)
	assert.Equal(t, []string{"push abc all green"}, h.commands.ran())
	assert.Equal(t, 1, h.gate.calls)
}

func TestWalk_AGateThatCannotRunIsAnErrorAndSpendsNoRound(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.gate.err = errFake
	text := `workflow "w" {}
loop "again" {
  max   = 3
  until = gate.check.passed

  gate "check" {}
}
`

	_, err := walkText(t, h, text)

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, `gate "check" could not run`)
	assert.Equal(t, 1, h.gate.calls, "the loop does not go on to another round")
}

func TestWalk_AGateWithARunPassesOnExitZeroAndKeepsTheReport(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		return CommandOutput{Stdout: "the report", RC: map[string]int{"yes": 0, "no": 2}[request.Run[0]]}, nil
	}
	text := `workflow "w" {}
gate "yes" {
  run  = ["yes"]
  head = "h1"
}
gate "no" {
  run = ["no"]
}
command "after" {
  run  = ["after", gate.yes.head, gate.yes.report]
  when = gate.yes.passed && !gate.no.passed
}
`

	_, err := walkText(t, h, text)

	require.NoError(t, err)
	assert.Equal(t, []string{"yes", "no", "after h1 the report"}, h.commands.ran())
}

func TestWalk_AGateThatCannotStartItsProgramIsAnErrorAndAHeadThatIsNotTextIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{}, errFake }

	_, err := walkText(t, h, "workflow \"w\" {}\ngate \"g\" {\n  run = [\"g\"]\n}\n")
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, `gate "g" could not run`)

	other := newHarness(t)

	_, err = walkText(t, other, "workflow \"w\" {}\ngate \"g\" {\n  run  = [\"g\"]\n  head = 5\n}\n")
	require.ErrorContains(t, err, "must be a string")

	third := newHarness(t)

	_, err = walkText(t, third, "workflow \"w\" {}\ngate \"g\" {\n  run = \"g\"\n}\n")
	require.ErrorContains(t, err, "must be a list of strings")
}

func TestWalk_AWaitReturnsTheAnswerOfTheOwnerAndShowsItsQuestion(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	text := `workflow "w" {}
wait "ask" {
  question = "Which branch?"
}
command "use" {
  run = ["use", wait.ask.answer]
}
`

	file := writeFlow(t, text)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool {
		rows, err := client.Status()

		return err == nil && len(rows) == 1 && rows[0].Question == "Which branch?"
	})
	eventually(t, func() bool { return client.Answer(testItem, "main") == nil })

	outcome := <-done

	require.NoError(t, outcome.err)
	assert.Equal(t, []string{"use main"}, h.commands.ran())
}

func TestWalk_AWaitWithNoQuestionAsksTheDefaultQuestion(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	file := writeFlow(t, "workflow \"w\" {}\nwait \"ask\" {}\n")
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool {
		rows, err := client.Status()

		return err == nil && len(rows) == 1 && rows[0].Question == "The workflow waits for your answer."
	})
	eventually(t, func() bool { return client.Answer(testItem, "go") == nil })
	require.NoError(t, (<-done).err)
}

func TestWalk_AWaitWithAQuestionThatIsNotTextIsRefused(t *testing.T) {
	t.Parallel()

	_, err := walkText(t, newHarness(t), "workflow \"w\" {}\nwait \"ask\" {\n  question = 5\n}\n")

	require.ErrorContains(t, err, "must be a string")
}

func TestWalk_TheFieldsOfTheItemFillTheItemObject(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\", item.title]\n}\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"a a title"}, h.commands.ran())

	failing := newHarness(t)
	failing.items.err = errFake

	_, err = walkText(t, failing, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n}\n")

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the fields of item 12")
}

func TestWalk_AFileThatIsMissingOrDoesNotParseFailsTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, filepath.Join(t.TempDir(), "missing.hcl"))
	require.ErrorContains(t, err, "read the workflow file")

	other := newHarness(t)

	_, err = walkText(t, other, "workflow {{{")
	require.Error(t, err)
	assert.Empty(t, other.commands.ran())
}

func TestRunFile_PinsTheAbsolutePathOfARelativeFile(t *testing.T) {
	h := newHarness(t)
	engine := h.engine(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.hcl"), []byte("workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n}\n"), 0o600))
	t.Chdir(dir)

	_, err := engine.RunFile(testItem, testProject, "workflow.hcl")

	require.NoError(t, err)

	input, err := dbos.RetrieveWorkflow[ItemResult](engine.ctx, testRun)
	require.NoError(t, err)

	status, err := input.GetStatus()
	require.NoError(t, err)

	text, ok := status.Input.(string)
	require.True(t, ok, "%T", status.Input)

	var pinned ItemInput
	require.NoError(t, json.Unmarshal([]byte(text), &pinned))
	assert.True(t, filepath.IsAbs(pinned.File))
	assert.NotEmpty(t, pinned.Hash)
}

func TestExecutorFor_RefusesAStepOfAnotherKind(t *testing.T) {
	t.Parallel()

	step := loadStep(t, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n}\n")

	_, err := executorFor(workflow.KindGate)(&call{step: step})

	require.ErrorContains(t, err, "step a is a command step, and this executor does not run it")
}

func TestRunFile_RefusesABadItemAndAMissingProject(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	file := writeFlow(t, "workflow \"w\" {}\n")

	_, err := engine.RunFile("zz", testProject, file)
	require.ErrorIs(t, err, ErrBadItem)

	_, err = engine.RunFile(testItem, "", file)
	require.ErrorIs(t, err, errNoProject)
}

// interrupted starts the engine over the harness, runs the file until its wait asks, and stops the engine as a crash does.
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

const crashFlow = `workflow "w" {}
command "before" {
  run = ["before"]
}
wait "ask" {
  question = "go?"
}
command "after" {
  run = ["after", wait.ask.answer]
}
`

func TestWalk_AReplayIssuesTheSameStepNamesAndRunsNoRecordedStepAgain(t *testing.T) {
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
	assert.Equal(t, []string{"before#1", "ask#1", "after#1"}, stepNames(t, next))
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

const envFlow = `workflow "w" {}
command "before" {
  run = ["before"]
}
wait "ask" {
  question = "go?"
}
command "after" {
  run  = ["after", wait.ask.answer]
  when = env.MODE == "full"
}
`

func TestWalk_AReplayTakesTheBranchesOfTheEnvironmentTheRunStartedWith(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"MODE": "full", "SECRET": "hunter2"}
	file := writeFlow(t, envFlow)
	interrupted(t, h, file)

	h.opts.Env = map[string]string{"MODE": "other"}
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
	assert.Equal(t, []string{"before", "after yes"}, h.commands.ran(), "the replay takes the branch of the first run")

	steps, err := dbos.GetWorkflowSteps(next.ctx, testRun)
	require.NoError(t, err)

	var recorded string

	for _, step := range steps {
		if step.StepName == "env" {
			recorded = fmt.Sprint(step.Output)
		}
	}

	assert.Contains(t, recorded, "full")
	assert.NotContains(t, recorded, "hunter2", "only the variables that the file reads are recorded")
}

const nestedFlow = `workflow "w" {}
loop "outer" {
  max   = 2
  until = loop.round == 9

  loop "inner" {
    max   = 2
    until = loop.round == 9

    command "a" {
      run = ["a"]
    }
  }
}
wait "ask" {
  question = "go?"
}
command "after" {
  run = ["after", wait.ask.answer]
}
`

func TestWalk_AStepNameInNestedLoopsCarriesEveryRoundAndIsTheSameOnReplay(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := writeFlow(t, nestedFlow)
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
	assert.Equal(t, []string{"a", "a", "a", "a", "after yes"}, h.commands.ran(), "the loops ran once, before the crash")
	assert.Equal(t, []string{"a#1.1", "a#1.2", "a#2.1", "a#2.2", "ask#1", "after#1"}, stepNames(t, next))
}

func TestWalk_AChangedFileRefusesTheReplayWithBothHashes(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := writeFlow(t, crashFlow)
	interrupted(t, h, file)

	before := sha256.Sum256([]byte(crashFlow))
	changed := crashFlow + "# edited\n"
	after := sha256.Sum256([]byte(changed))
	require.NoError(t, os.WriteFile(file, []byte(changed), 0o600))

	_, err := runFileWithin(t, h.engine(t), file)

	require.ErrorContains(t, err, file+" changed since the run started")
	require.ErrorContains(t, err, hex.EncodeToString(before[:]))
	require.ErrorContains(t, err, hex.EncodeToString(after[:]))
	require.ErrorContains(t, err, "Restore the file, or cancel the run and start again")
	assert.Equal(t, []string{"before"}, h.commands.ran(), "no step ran on the changed file")
}

func TestPinnedFile_NamesTheFileOfTheOpenRunAndNothingElse(t *testing.T) {
	t.Parallel()

	none, err := newHarness(t).engine(t).PinnedFile(testItem)
	require.NoError(t, err)
	assert.Empty(t, none, "no run is open")

	h := newHarness(t)
	file := writeFlow(t, crashFlow)
	interrupted(t, h, file)

	pinned, err := h.engine(t).PinnedFile(testItem)

	require.NoError(t, err)
	assert.Equal(t, file, pinned)
}

func TestWalk_ARemovedFileRefusesTheReplay(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := writeFlow(t, crashFlow)
	interrupted(t, h, file)
	require.NoError(t, os.Remove(file))

	engine := h.engine(t) // Launch recovers the open run, which reads the file again.
	handle, err := dbos.RetrieveWorkflow[ItemResult](engine.ctx, testRun)
	require.NoError(t, err)

	_, err = handle.GetResult()

	require.ErrorContains(t, err, "read the workflow file")
	require.ErrorContains(t, err, "Restore the file")
}

func loadStep(t *testing.T, text string) workflow.Step {
	t.Helper()

	loaded, err := workflow.Load(fstest.MapFS{"w.hcl": {Data: []byte(text)}}, "w.hcl")
	require.NoError(t, err)

	return loaded.Steps[0]
}

func TestRecord_RefusesAnOutputTheKindDoesNotDeclare(t *testing.T) {
	t.Parallel()

	step := loadStep(t, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n}\n")
	w := &walker{scope: workflow.NewScope(nil, nil)}

	err := w.record(step, stepResult{Outputs: map[string]any{"bogus": 1}})

	require.ErrorContains(t, err, `step a: a command step has no output "bogus"`)
}

func TestTyped_ConvertsEachOutputByItsDeclaredType(t *testing.T) {
	t.Parallel()

	step := loadStep(t, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n}\n")

	got, err := typed(step, map[string]any{"stdout": "x", "rc": float64(3)})
	require.NoError(t, err)
	assert.Equal(t, "x", got["stdout"].AsString())

	assert.True(t, cty.NumberIntVal(3).RawEquals(got["rc"]))

	_, err = typed(step, map[string]any{"rc": "three"})
	require.ErrorContains(t, err, `output "rc" of a command step: the value three is not a number`)
}

func TestToValue_AcceptsTheTypesOfAStoredResultAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	ok := []struct {
		want cty.Type
		in   any
		out  cty.Value
	}{
		{cty.String, nil, cty.NullVal(cty.String)},
		{cty.String, "s", cty.StringVal("s")},
		{cty.Bool, true, cty.True},
		{cty.Number, 4, cty.NumberIntVal(4)},
		{cty.Number, 2.5, cty.NumberFloatVal(2.5)},
	}
	for _, c := range ok {
		got, err := toValue(c.want, c.in)
		require.NoError(t, err)
		assert.True(t, c.out.RawEquals(got), "%v", c.in)
	}

	for _, c := range []struct {
		want cty.Type
		in   any
	}{
		{cty.Number, "s"},
		{cty.String, true},
		{cty.String, 4},
		{cty.String, 2.5},
		{cty.String, []string{"x"}},
	} {
		_, err := toValue(c.want, c.in)
		require.ErrorContains(t, err, "is not a")
	}
}

func TestExecutorFor_NamesAnExecutorForEveryKindThatRunsInOneNode(t *testing.T) {
	t.Parallel()

	for _, kind := range []workflow.Kind{workflow.KindCommand, workflow.KindGate, workflow.KindWait, workflow.KindAgent} {
		assert.NotNil(t, executorFor(kind), kind)
	}

	assert.Nil(t, executorFor(workflow.KindLoop), "the walker runs a loop itself")
}
