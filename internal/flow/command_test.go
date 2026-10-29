package flow

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var settingsFlow = bodyFlow(`command(
    run = ["report"],
    stdin = "the text",
    env = {"MODE": "strict", "AREA": "all"},
    name = "report",
)
`)

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

	_, err := walkBody(t, h, "command(run = [\"a\"])\n")

	require.NoError(t, err)
	assert.Equal(t, CommandRequest{Project: testProject, Item: testItem, Run: []string{"a"}, Env: []string{"HOME=/home/me"}}, h.commands.requests[0])
}

func TestWalk_AnEmptyEnvDictAddsNoVariable(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"NAME": "x", "HOME": "/h"}

	_, err := walkBody(t, h, "command(run = [\"a\"], env = {} if env(\"NAME\") == \"x\" else {\"A\": \"1\"})\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"HOME=/h"}, h.commands.requests[0].Env)
}

func TestWalk_AHostCommandWithStdinOrEnvIsRefusedBeforeItRuns(t *testing.T) {
	t.Parallel()

	for name, setting := range map[string]string{"stdin": `stdin = "x"`, "env": `env = {"A": "1"}`} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			_, err := walkBody(t, h, "command(run = [\"a\"], host = \"box\", "+setting+")\n")

			require.ErrorContains(t, err, "host")
			assert.Empty(t, h.commands.requests)
		})
	}
}

func TestWalk_AGateGetsNoEnvironmentOfItsOwn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkBody(t, h, "gate(run = [\"check\"])\n")

	require.NoError(t, err)
	assert.Nil(t, h.commands.requests[0].Env)
}

func TestWalk_ASettingThatIsWrongFailsTheStepBeforeItRuns(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"env name OBEE_":       `env = {"OBEE_X": "1"}`,
		"env name DBOS__":      `env = {"DBOS__X": "1"}`,
		"env value not string": `env = {"A": 1}`,
		"env not a dict":       `env = "x"`,
		"stdin not a string":   `stdin = 1`,
		"stop code is 0":       `stop_on_rc = [0]`,
		"stop code above 255":  `stop_on_rc = [256]`,
		"stop code a fraction": `stop_on_rc = [1.5]`,
		"stop code not number": `stop_on_rc = ["two"]`,
		"stop not a list":      `stop_on_rc = 2`,
		"timeout not a time":   `timeout = "soon"`,
		"run not a list":       `run = "a"`,
		"run holds a number":   `run = [1]`,
	}

	for name, setting := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			engine := h.engine(t)

			args := `run = ["a"], ` + setting
			if setting[:3] == "run" {
				args = setting
			}

			_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("command("+args+")\n")))

			require.Error(t, err)
			assert.Empty(t, h.commands.requests, "the program did not start")
			assert.Empty(t, stepNames(t, engine), "the setting is checked before the durable step, so a failure leaves no record")
		})
	}
}

func TestWalk_AnEnvNameThatIsReservedNamesTheNameAndTheSetting(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Env = map[string]string{"NAME": "OBEE_X"}

	_, err := walkBody(t, h, "command(run = [\"a\"], env = {env(\"NAME\"): \"1\"})\n")

	require.ErrorContains(t, err, "OBEE_X")
	require.ErrorContains(t, err, "env")
}

var stopFlow = bodyFlow(`check = command(run = ["check"], stop_on_rc = [2, 3], name = "check")
if check.rc == 1:
    command(run = ["fix", check.stdout], name = "fix")
command(run = ["done"], name = "done")
`)

func TestWalk_ACodeInStopOnRCEndsTheItemWithTheOutputOfTheStepAndRunsNothingAfter(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{RC: 2, Stdout: "refused: no scope\n"}, nil
	}

	result, err := walkText(t, h, stopFlow)

	require.NoError(t, err)
	assert.Equal(t, &Stopped{Step: "command:check#1", RC: 2, Output: "refused: no scope\n"}, result.Stopped)
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

	result, err := walkText(t, h, stopFlow)

	require.NoError(t, err)
	assert.Nil(t, result.Stopped)
	assert.Equal(t, []string{"check", "fix defect: 3 lines", "done"}, h.commands.ran())
}

func TestWalk_ACodeZeroPassesAStepThatListsStopCodes(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	result, err := walkText(t, h, stopFlow)

	require.NoError(t, err)
	assert.Nil(t, result.Stopped)
	assert.Equal(t, []string{"check", "done"}, h.commands.ran(), "fix is skipped because rc is not 1")
}

func TestWalk_AStepInsideALoopThatStopsEndsTheItemAtOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = lintRCs(1, 2)

	result, err := walkBody(t, h, `for _ in range(5):
    lint = command(run = ["lint"], stop_on_rc = [2], name = "lint")
    if lint.rc == 0:
        break
command(run = ["after"], name = "after")
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
	file := writeFlow(t, stopFlow)
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
	assert.Equal(t, &Stopped{Step: "command:check#1", RC: 2, Output: "first answer"}, result.Stopped, "the replay reads the record, not a second run")
	assert.Equal(t, []string{"check"}, h.commands.ran())
}

var replayInputFlow = bodyFlow(`before = command(run = ["before"], name = "before")
ask = wait("go?", name = "ask")
command(
    run = ["after"],
    stdin = before.stdout,
    env = {"ANSWER": ask.answer, "FROM": before.stdout},
    name = "after",
)
`)

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

func TestWalk_AStopCodeThatNoProgramCanReturnIsRefusedBeforeTheProgramRuns(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"256", "0", "-1"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			_, err := walkBody(t, h, "command(run = [\"a\"], stop_on_rc = ["+code+"])\n")

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

	_, err := walkText(t, h, stopFlow)
	require.NoError(t, err)

	infos, err := h.client(t, testVersion).Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, RunInfo{
		ID: "item-12-r1", State: runStopped, Version: testVersion,
		Stopped: &Stopped{Step: "command:check#1", RC: 2, Output: "refused: no scope\n"},
	}, infos[0])
}
