package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
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

func TestCommandRetries_EachTryIsARecordOfItsOwnAndTheCallEndsInOneRecord(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = codes(7)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("command(run = [\"fetch\"], retries = 2, backoff = \"1ms\", name = \"fetch\")\n")), modeOff())

	require.NoError(t, err)
	assert.Equal(t, 3, timesRun(h, "fetch"))
	assert.Equal(t, []string{"command:fetch#1/try1", "command:fetch#1/try2", "command:fetch#1/try3", "command:fetch#1"}, stepNames(t, engine),
		"a replay reads each try and the record of the call")
}

func TestCommandRetries_ACallWithNoRetriesIsOneRecord(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = codes(7)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("command(run = [\"fetch\"], name = \"fetch\")\n")), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"command:fetch#1"}, stepNames(t, engine))
}

func TestCommandRetries_TheRunViewShowsARetriedCallAsOneLine(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = codes(1, 0)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("command(run = [\"fetch\"], retries = 2, backoff = \"1ms\", name = \"fetch\")\n")), modeOff())
	require.NoError(t, err)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	var fetch []StepLine

	for _, line := range stepLines(steps, runDone, false, time.Now()) {
		if strings.HasPrefix(line.Name, "command:fetch") {
			fetch = append(fetch, line)
		}
	}

	require.Len(t, fetch, 1, "the tries and the wait belong to the line of the call")
	assert.Equal(t, "command:fetch#1", fetch[0].Name)
	assert.Equal(t, stepDone, fetch[0].State)
}

func TestCommandRetries_TheDoubledWaitStopsAtTheLongestWait(t *testing.T) {
	t.Parallel()

	minute := workflow.RetryPolicy{Retries: workflow.MaxRetries, Backoff: time.Minute}
	first, most := retryWaits(minute)

	assert.Equal(t, time.Minute, first)
	assert.Equal(t, 10*time.Minute, most, "ten retries from a minute would wait 512 minutes before the last one with no cap")
	assert.Equal(t, []time.Duration{time.Minute, 2 * time.Minute, 8 * time.Minute, 10 * time.Minute},
		[]time.Duration{retryWait(minute, 1), retryWait(minute, 2), retryWait(minute, 4), retryWait(minute, 5)})

	first, most = retryWaits(workflow.RetryPolicy{Retries: 2})
	assert.Equal(t, []time.Duration{100 * time.Millisecond, 5 * time.Second}, []time.Duration{first, most}, "a call with no backoff starts at 100 ms and stops at 5 s")
}

func TestCommandRetries_TheWaitDoublesPastFiveSeconds(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		starts []time.Time
	)

	h := newHarness(t)
	next := codes(1, 1, 1, 0)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "fetch" {
			mu.Lock()
			starts = append(starts, time.Now())
			mu.Unlock()
		}

		return next(request)
	}

	// The waits are 1.5 s, 3 s and 6 s. With a longest wait of 5 s, the third would be 5 s.
	_, err := walkBody(t, h, "command(run = [\"fetch\"], retries = 3, backoff = \"1500ms\", name = \"fetch\")\n")

	require.NoError(t, err)
	require.Len(t, starts, 4)
	assert.GreaterOrEqual(t, starts[3].Sub(starts[2]), 5500*time.Millisecond, "the third wait doubles past five seconds")
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

func TestCommandRetries_ACrashDuringTry2RunsTry2AgainAndKeepsTheCount(t *testing.T) {
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

	assert.Equal(t, 3, timesRun(h, "fetch"), "two tries before the crash, and try 2 again after it")
	assert.Equal(t, "log 0 2", logged(h), "try 1 is recorded, so the resume runs try 2 and the call counts two tries")
}

func TestCommandRetries_ACrashDuringTheWaitResumesTheWaitAndRunsTheNextTryOnce(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		starts []time.Time
	)

	h := newHarness(t)
	crash := newCrash(t, h)
	fetch := codes(1, 0)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		out, err := fetch(request)
		if request.Run[0] != "fetch" {
			return out, err
		}

		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()

		if out.Stdout == "try 1" {
			// The record of try 1 is written well within the second; the crash then comes in the wait of 4 s.
			time.AfterFunc(time.Second, crash.after)
		}

		return out, err
	}

	crashAndResume(t, h, h.deps(), "c = command(run = [\"fetch\"], retries = 1, backoff = \"4s\", name = \"fetch\")\ncommand(run = [\"log\", str(c.rc), str(c.attempts)], name = \"log\")\n")

	assert.Equal(t, 2, timesRun(h, "fetch"), "the crash comes in the wait after try 1, so the resume runs try 2 only")
	assert.Equal(t, "log 0 2", logged(h))
	require.Len(t, starts, 2)
	assert.GreaterOrEqual(t, starts[1].Sub(starts[0]), 4*time.Second, "the resume waits the rest of the wait before try 2")
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

	tries := tries{failure: workflow.FailureReturn}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	assert.False(t, tries.returned(canceled, 1, errors.New("x"), func(error) bool { return false }), "the context of the step ended")
	assert.False(t, tries.returned(t.Context(), 1, fmt.Errorf("stop: %w", context.Canceled), func(error) bool { return false }), "the error is a cancel")
	assert.True(t, tries.returned(t.Context(), 1, errors.New("x"), func(error) bool { return false }))
}

func TestCommandRetries_ACancelOfTheRunIsNeverTriedAgain(t *testing.T) {
	t.Parallel()

	tries := tries{policy: workflow.RetryPolicy{Retries: 2}}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	assert.False(t, tries.again(canceled, 1, errors.New("x"), func(error) bool { return true }), "the context of the step ended")
	assert.False(t, tries.again(t.Context(), 1, fmt.Errorf("stop: %w", context.Canceled), func(error) bool { return true }), "the error is a cancel")
	assert.False(t, tries.again(t.Context(), 3, errors.New("x"), func(error) bool { return true }), "no try is left")
	assert.True(t, tries.again(t.Context(), 2, errors.New("x"), func(error) bool { return true }))
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
