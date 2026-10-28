package flow

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const settingsFlow = `workflow "w" {}
command "report" {
  run   = ["report"]
  stdin = "the text"
  env   = { MODE = "strict", AREA = "all" }
}
`

func TestWalk_ACommandPassesItsStdinAndAnEnvironmentOfAllowedNamesAndItsOwnVariables(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"PATH": "/bin", "LC_ALL": "C", "GH_TOKEN": "secret", "OBEE_STATE_DIR": "/state", "MODE": "loose"}

	_, err := walkText(t, h, settingsFlow)

	require.NoError(t, err)
	require.Len(t, h.commands.requests, 1)

	got := h.commands.requests[0]
	assert.Equal(t, "the text", got.Stdin)
	assert.Equal(t, []string{"LC_ALL=C", "PATH=/bin", "AREA=all", "MODE=strict"}, got.Env, "the allowed names of the caller, then the variables of the step; no token, no OBEE_ or DBOS__ name")
}

func TestWalk_ACommandWithNoSettingsGetsTheAllowedEnvironmentAndNoStdin(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"HOME": "/home/me", "ANTHROPIC_API_KEY": "secret"}

	_, err := walkText(t, h, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n}\n")

	require.NoError(t, err)
	assert.Equal(t, CommandRequest{Project: testProject, Item: testItem, Run: []string{"a"}, Env: []string{"HOME=/home/me"}}, h.commands.requests[0])
}

func TestWalk_AnEnvThatComputesToNothingAddsNoVariable(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"NAME": "x", "HOME": "/h"}

	_, err := walkText(t, h, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n  env = env.NAME == \"x\" ? null : { A = \"1\" }\n}\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"HOME=/h"}, h.commands.requests[0].Env)
}

func TestWalk_AHostCommandWhoseStdinOrEnvAnExpressionComputesIsRefusedBeforeItRuns(t *testing.T) {
	t.Parallel()

	for name, setting := range map[string]string{"stdin": "stdin = env.NAME", "env": "env = { A = env.NAME }"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.opts.Env = map[string]string{"NAME": "x"}

			_, err := walkText(t, h, "workflow \"w\" {}\ncommand \"a\" {\n  run  = [\"a\"]\n  host = \"box\"\n  "+setting+"\n}\n")

			require.ErrorContains(t, err, "host")
			assert.Empty(t, h.commands.requests)
		})
	}
}

func TestWalk_AGateGetsNoEnvironmentOfItsOwn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "workflow \"w\" {}\ngate \"g\" {\n  run = [\"check\"]\n}\n")

	require.NoError(t, err)
	assert.Nil(t, h.commands.requests[0].Env)
}

func TestWalk_ASettingThatAnExpressionComputesBadlyFailsTheStepBeforeItRuns(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		setting string
		text    string
	}{
		"env name OBEE_":       {`env = { (env.NAME) = "1" }`, "OBEE_X"},
		"env name DBOS__":      {`env = { (env.NAME) = "1" }`, "DBOS__X"},
		"env value not string": {`env = { A = 1 + env.NAME }`, "2"},
		"env not a map":        {`env = env.NAME`, "x"},
		"stdin not a string":   {`stdin = 1 + env.NAME`, "2"},
		"stop code is 0":       {`stop_on_rc = [0 + env.NAME]`, "0"},
		"stop code above 255":  {`stop_on_rc = [0 + env.NAME]`, "256"},
		"stop code a fraction": {`stop_on_rc = [0 + env.NAME]`, "1.5"},
		"stop code not number": {`stop_on_rc = [env.NAME]`, "two"},
		"stop not a list":      {`stop_on_rc = env.NAME`, "2"},
		"env cannot compute":   {`env = { A = 1 + env.NAME }`, "x"},
		"stop cannot compute":  {`stop_on_rc = [0 + env.NAME]`, "x"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.opts.Env = map[string]string{"NAME": test.text}

			_, err := walkText(t, h, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n  "+test.setting+"\n}\n")

			require.Error(t, err)
			assert.Contains(t, err.Error(), "step a#1")
			assert.Empty(t, h.commands.requests, "the program did not start")
		})
	}
}

func TestWalk_AnEnvNameFromAnExpressionThatIsReservedNamesTheNameAndTheSetting(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"NAME": "OBEE_X"}

	_, err := walkText(t, h, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n  env = { (env.NAME) = \"1\" }\n}\n")

	require.ErrorContains(t, err, "OBEE_X")
	require.ErrorContains(t, err, "env")
}

const stopFlow = `workflow "w" {}
command "check" {
  run        = ["check"]
  stop_on_rc = [2, 3]
}
command "fix" {
  run  = ["fix", command.check.stdout]
  when = command.check.rc == 1
}
command "done" {
  run = ["done"]
}
`

func stopFlowText() string { return stopFlow }

func TestWalk_ACodeInStopOnRCEndsTheItemWithTheOutputOfTheStepAndRunsNothingAfter(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{RC: 2, Stdout: "refused: no scope\n"}, nil
	}

	result, err := walkText(t, h, stopFlowText())

	require.NoError(t, err)
	assert.Equal(t, &Stopped{Step: "check", RC: 2, Output: "refused: no scope\n"}, result.Stopped)
	assert.Equal(t, []string{"check"}, h.commands.ran())
}

func TestWalk_ACodeOutsideStopOnRCGoesToTheFixAndTheRunGoesOn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "check" {
			return CommandOutput{RC: 1, Stdout: "defect: 3 lines"}, nil
		}

		return CommandOutput{}, nil
	}

	result, err := walkText(t, h, stopFlowText())

	require.NoError(t, err)
	assert.Nil(t, result.Stopped)
	assert.Equal(t, []string{"check", "fix defect: 3 lines", "done"}, h.commands.ran())
}

func TestWalk_ACodeZeroPassesAStepThatListsStopCodes(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	result, err := walkText(t, h, stopFlowText())

	require.NoError(t, err)
	assert.Nil(t, result.Stopped)
	assert.Equal(t, []string{"check", "done"}, h.commands.ran(), "fix is skipped because rc is not 1")
}

func TestWalk_AStepInsideALoopThatStopsEndsTheItemAtOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = lintRCs(1, 2)

	result, err := walkText(t, h, `workflow "w" {}
loop "tidy" {
  max   = 5
  until = command.lint.rc == 0

  command "lint" {
    run        = ["lint"]
    stop_on_rc = [2]
  }
}
command "after" {
  run = ["after"]
}
`)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Equal(t, []string{"lint", "lint"}, h.commands.ran())
}

func TestWalk_AStepThatStoppedOnACodeStaysStoppedOnReplayAndRunsNoMoreThanOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{RC: 2, Stdout: "first answer"}, nil }
	h.life.failing["release"] = true
	file := writeFlow(t, stopFlowText())
	engine := h.engine(t)

	// The release after the last step fails, so the run fails with the record of the stopped step in its history.
	_, err := engine.RunFile(testItem, testProject, file)
	require.ErrorContains(t, err, "release")

	h.life.mu.Lock()
	h.life.failing["release"] = false
	h.life.mu.Unlock()

	h.commands.mu.Lock()
	h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{RC: 0, Stdout: "second answer"}, nil }
	h.commands.mu.Unlock()

	require.NoError(t, h.client(t, testVersion).Resume(testItem))

	result, err := engine.RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, &Stopped{Step: "check", RC: 2, Output: "first answer"}, result.Stopped, "the replay reads the record, not a second run")
	assert.Equal(t, []string{"check"}, h.commands.ran())
}

const replayInputFlow = `workflow "w" {}
command "before" {
  run = ["before"]
}
wait "ask" {
  question = "go?"
}
command "after" {
  run   = ["after"]
  stdin = command.before.stdout
  env   = { ANSWER = wait.ask.answer, FROM = command.before.stdout }
}
`

func TestWalk_AReplayBuildsTheSameStdinAndEnvFromTheRecordedResults(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	calls := 0
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		calls++

		return CommandOutput{Stdout: request.Run[0] + "-run-" + strconv.Itoa(calls)}, nil
	}

	file := writeFlow(t, replayInputFlow)
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
	require.Len(t, h.commands.requests, 2, "the command before the wait ran once")
	assert.Equal(t, "before-run-1", h.commands.requests[1].Stdin)
	assert.Equal(t, []string{"ANSWER=yes", "FROM=before-run-1"}, h.commands.requests[1].Env)
}

func TestWalk_ASettingThatCannotBeEvaluatedLeavesNoRecordOfTheStep(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"NAME": "OBEE_X"}
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n  env = { (env.NAME) = \"1\" }\n}\n"))

	require.Error(t, err)
	assert.Empty(t, stepNames(t, engine), "the settings are evaluated before the durable step")
}

func TestWalk_AStdinEnvOrStopCodeThatCannotBeEvaluatedLeavesNoRecordOfTheStep(t *testing.T) {
	t.Parallel()

	for name, setting := range map[string]string{
		"stdin": `stdin = 1 + env.NAME`,
		"env":   `env = { A = 1 + env.NAME }`,
		"stop":  `stop_on_rc = [0 + env.NAME]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.opts.Env = map[string]string{"NAME": "x"}
			engine := h.engine(t)

			_, err := engine.RunFile(testItem, testProject, writeFlow(t, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n  "+setting+"\n}\n"))

			require.Error(t, err)
			assert.Empty(t, stepNames(t, engine), "the setting is evaluated before the durable step, so a failure leaves no record")
			assert.Empty(t, h.commands.requests)
		})
	}
}

func TestWalk_AStopCodeThatNoProgramCanReturnIsRefusedBeforeTheProgramRuns(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"256", "0", "-1"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			_, err := walkText(t, h, "workflow \"w\" {}\ncommand \"a\" {\n  run = [\"a\"]\n  stop_on_rc = ["+code+"]\n}\n")

			require.Error(t, err)
			assert.Contains(t, err.Error(), "1 to 255")
			assert.Empty(t, h.commands.requests, "the program did not start")
		})
	}
}

func TestRunInfo_AStoppedItemShowsTheStepAndTheCode(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{RC: 2, Stdout: "refused: no scope\n"}, nil
	}

	_, err := walkText(t, h, stopFlowText())
	require.NoError(t, err)

	infos, err := h.client(t, testVersion).Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, RunInfo{
		ID: "item-12-r1", State: runDone, Version: testVersion,
		Stopped: &Stopped{Step: "check", RC: 2, Output: "refused: no scope\n"},
	}, infos[0])
}
