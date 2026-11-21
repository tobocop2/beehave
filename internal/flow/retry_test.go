package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/workflow"
)

// codes replies to each request of the program fetch with the next exit code of the list, then with the last one. Other programs exit with 0.
func codes(list ...int) func(CommandRequest) (CommandOutput, error) {
	var (
		mu   sync.Mutex
		next int
	)

	return func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] != "fetch" {
			return CommandOutput{}, nil
		}

		mu.Lock()
		defer mu.Unlock()

		rc := list[min(next, len(list)-1)]
		next++

		return CommandOutput{RC: rc, Stdout: fmt.Sprintf("try %d", next)}, nil
	}
}

// runsOf counts the requests of a program.
func timesRun(h *harness, program string) int {
	n := 0

	for _, line := range h.commands.ran() {
		if strings.Fields(line)[0] == program {
			n++
		}
	}

	return n
}

// logged is the last request of the program log, which a flow uses to show the fields of a result.
func logged(h *harness) string {
	ran := h.commands.ran()
	for i := len(ran) - 1; i >= 0; i-- {
		if strings.HasPrefix(ran[i], "log ") {
			return ran[i]
		}
	}

	return ""
}

func TestCommandRetries_AProgramThatFailsTwiceAndThenPassesReturnsRCZeroAfterThreeAttempts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = codes(1, 1, 0)

	_, err := walkBody(t, h, "c = command(run = [\"fetch\"], retries = 2, backoff = \"1ms\", name = \"fetch\")\ncommand(run = [\"log\", str(c.rc), str(c.attempts), c.stdout], name = \"log\")\n")

	require.NoError(t, err)
	assert.Equal(t, 3, timesRun(h, "fetch"))
	assert.Equal(t, "log 0 3 try 3", logged(h))
}

func TestCommandRetries_WhenTheAttemptsAreSpentTheLastExitCodeIsTheResult(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = codes(7)

	_, err := walkBody(t, h, "c = command(run = [\"fetch\"], retries = 1, backoff = \"1ms\", name = \"fetch\")\ncommand(run = [\"log\", str(c.rc), str(c.attempts)], name = \"log\")\n")

	require.NoError(t, err)
	assert.Equal(t, 2, timesRun(h, "fetch"))
	assert.Equal(t, "log 7 2", logged(h))
}

func TestCommandRetries_TheSpentRetriesLeaveOneRecordOfTheLastTry(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = codes(7)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("command(run = [\"fetch\"], retries = 2, backoff = \"1ms\", name = \"fetch\")\n")))

	require.NoError(t, err)
	assert.Equal(t, 3, timesRun(h, "fetch"))
	assert.Equal(t, []string{"command:fetch#1"}, stepNames(t, engine), "the step holds the last try, so a replay reads one record")
}

func TestCommandRetries_ACodeInStopOnRCIsNeverRetried(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = codes(9)

	result, err := walkBody(t, h, "command(run = [\"fetch\"], stop_on_rc = [9], retries = 3, backoff = \"1ms\", name = \"fetch\")\n")

	require.NoError(t, err)
	assert.Equal(t, 1, timesRun(h, "fetch"))
	require.NotNil(t, result.Stopped)
	assert.Equal(t, 9, result.Stopped.RC)
}

func TestCommandRetries_ACallWithNoRetriesRunsOnceAndHasOneAttempt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = codes(1)

	_, err := walkBody(t, h, "c = command(run = [\"fetch\"], name = \"fetch\")\ncommand(run = [\"log\", str(c.rc), str(c.attempts), repr(c.error)], name = \"log\")\n")

	require.NoError(t, err)
	assert.Equal(t, 1, timesRun(h, "fetch"))
	assert.Equal(t, `log 1 1 ""`, logged(h))
}

func TestCommandRetries_AnErrorOfTheStepIsRetriedAndRaisesWhenTheAttemptsAreSpent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{}, errors.New("the job queue did not answer")
	}

	_, err := walkBody(t, h, "command(run = [\"fetch\"], retries = 2, backoff = \"1ms\", name = \"fetch\")\ncommand(run = [\"log\"], name = \"log\")\n")

	require.ErrorContains(t, err, "the job queue did not answer")
	assert.Equal(t, 3, timesRun(h, "fetch"))
	assert.Zero(t, timesRun(h, "log"), "the error ends the run")
}

func TestCommandRetries_AKeptErrorIsNotRetried(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{}, fmt.Errorf("the host is gone: %w", host.ErrLost)
	}

	_, err := walkBody(t, h, "command(run = [\"fetch\"], retries = 3, backoff = \"1ms\", name = \"fetch\")\n")

	require.ErrorIs(t, err, host.ErrLost)
	assert.Equal(t, 1, timesRun(h, "fetch"))
}

func TestCommandRetries_AReplayAfterTheRetriesReadsTheRecordAndRunsTheProgramNoMore(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	crash := newCrash(t, h)
	fetch := codes(1, 0)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "later" {
			crash.after()
		}

		return fetch(request)
	}

	result := crashAndResume(t, h, h.deps(), "c = command(run = [\"fetch\"], retries = 1, backoff = \"1ms\", name = \"fetch\")\ncommand(run = [\"later\"], name = \"later\")\ncommand(run = [\"log\", str(c.rc), str(c.attempts)], name = \"log\")\n")

	assert.Equal(t, testItem, result.Item)
	assert.Equal(t, 2, timesRun(h, "fetch"), "the replay reads the recorded result of the retried step")
	assert.Equal(t, "log 0 2", logged(h))
}

func TestCommandRetries_ACrashDuringTheRetriesStartsTheCountAgain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	crash := newCrash(t, h)
	fetch := codes(1, 1, 0)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		out, err := fetch(request)
		if out.Stdout == "try 2" {
			crash.after()
		}

		return out, err
	}

	crashAndResume(t, h, h.deps(), "c = command(run = [\"fetch\"], retries = 3, backoff = \"1ms\", name = \"fetch\")\ncommand(run = [\"log\", str(c.rc), str(c.attempts)], name = \"log\")\n")

	assert.Equal(t, 3, timesRun(h, "fetch"), "two tries before the crash, one after it")
	assert.Equal(t, "log 0 1", logged(h), "the library records only the last try, so the count starts again at the resume")
}

func TestCommandFailure_ReturnGivesTheErrorAndRCMinusOneAndTheRunGoesOn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "sweep" {
			return CommandOutput{}, fmt.Errorf("the host is gone: %w", host.ErrLost)
		}

		return CommandOutput{}, nil
	}

	_, err := walkBody(t, h, "c = command(run = [\"sweep\"], failure = \"return\", name = \"sweep\")\ncommand(run = [\"log\", str(c.rc), str(c.attempts), c.error], name = \"log\")\n")

	require.NoError(t, err)
	assert.Equal(t, "log -1 1 the host is gone: "+host.ErrLost.Error(), logged(h))
}

func TestCommandFailure_ReturnComesAfterTheRetriesAreSpent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "sweep" {
			return CommandOutput{}, errors.New("cannot start")
		}

		return CommandOutput{}, nil
	}

	_, err := walkBody(t, h, "c = command(run = [\"sweep\"], retries = 1, backoff = \"1ms\", failure = \"return\", name = \"sweep\")\ncommand(run = [\"log\", str(c.rc), str(c.attempts), c.error], name = \"log\")\n")

	require.NoError(t, err)
	assert.Equal(t, 2, timesRun(h, "sweep"))
	assert.Equal(t, "log -1 2 cannot start", logged(h))
}

func TestCommandFailure_TheDefaultRaisesAndEndsTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "sweep" {
			return CommandOutput{}, errors.New("cannot start")
		}

		return CommandOutput{}, nil
	}

	_, err := walkBody(t, h, "command(run = [\"sweep\"], name = \"sweep\")\ncommand(run = [\"log\"], name = \"log\")\n")

	require.ErrorContains(t, err, "cannot start")
	assert.Zero(t, timesRun(h, "log"))
}

func TestCommandFailure_ACancelOfTheRunIsNeverReturned(t *testing.T) {
	t.Parallel()

	tries := &tries{failure: workflow.FailureReturn}
	tries.start()

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	assert.False(t, tries.returned(canceled, errors.New("x"), func(error) bool { return false }), "the context of the step ended")
	assert.False(t, tries.returned(t.Context(), fmt.Errorf("stop: %w", context.Canceled), func(error) bool { return false }), "the error is a cancel")
	assert.True(t, tries.returned(t.Context(), errors.New("x"), func(error) bool { return false }))
}

func TestCommandRetries_TheRunRefusesAValueThatTheCheckCannotSee(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"retries out of range":  "n = 11\ncommand(run = [\"a\"], retries = n)\n",
		"backoff with no retry": "w = \"1s\"\ncommand(run = [\"a\"], backoff = w)\n",
		"retries with slots":    "n = 1\ncommand(run = [\"a\"], retries = n, host = \"box\", slots = \"big\")\n",
		"an unknown failure":    "f = \"ignore\"\ncommand(run = [\"a\"], failure = f)\n",
		"spawn retries":         "n = -1\nspawn(items = [\"21\"], retries = n)\n",
		"write backoff":         "w = \"0s\"\nwrite(run = [\"a\"], retries = 1, backoff = w)\n",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			_, err := walkBody(t, h, body)

			require.Error(t, err)
			assert.Regexp(t, "retries|backoff|failure", err.Error())
			assert.Empty(t, h.commands.requests, "nothing runs")
		})
	}
}

func TestWriteRetries_EachTryReadsBackFirstAndTheResultCountsTheAttempts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	pushes := 0
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		switch request.Run[0] {
		case "check":
			return CommandOutput{RC: 1}, nil
		case "push":
			pushes++

			return CommandOutput{RC: pushes % 2}, nil
		}

		return CommandOutput{}, nil
	}

	_, err := walkBody(t, h, "w = write(run = [\"push\"], read_back = [\"check\"], retries = 2, backoff = \"1ms\", name = \"w\")\ncommand(run = [\"log\", str(w.rc), str(w.attempts)], name = \"log\")\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"check", "push", "check", "push"}, h.commands.ran()[:4])
	assert.Equal(t, "log 0 2", logged(h))
}

func TestWriteRetries_ASkippedWriteCountsItsAttempt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := walkBody(t, h, "w = write(run = [\"push\"], read_back = [\"check\"], retries = 2, name = \"w\")\ncommand(run = [\"log\", str(w.skipped), str(w.attempts)], name = \"log\")\n")

	require.NoError(t, err)
	assert.Equal(t, "log True 1", logged(h))
}
