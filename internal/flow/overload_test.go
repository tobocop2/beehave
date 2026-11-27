package flow

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// overloaded is a run that the provider stopped with an overload, as the runner reports it: the outcome limited and a limit with no reset time.
func overloaded() agent.Result {
	return agent.Result{
		Outcome: agent.OutcomeLimited, ExitCode: 1, Session: "sess-overloaded", Note: "the provider refused more work at its overload, and the agent gave no reset time",
		Limit: &agent.Limit{Kind: agent.LimitOverload},
	}
}

func TestOverload_AnOverloadThatClearsAfterOneBackoffRunsTheStepAgainOnTheSameLogin(t *testing.T) {
	t.Parallel()

	var asked []string

	h := limitedHarness(t, limitReset)
	h.logins = fakeLogins{login: testLogin, reset: limitReset.Add(24 * time.Hour), asked: &asked}
	inTurn(h, overloaded(), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)

	requests := h.agents.seen()
	require.Len(t, requests, 2, "the overloaded run, then the rerun after one backoff")
	assert.Equal(t, requests[0].Login, requests[1].Login, "the rerun keeps the login")
	assert.Empty(t, asked, "the usage windows of the pool say nothing about an overload")

	steps := recordsOfTestRun(t, engine)
	assert.Equal(t, 1, sleepsIn(steps), "one durable sleep, the first step of the backoff")

	record := firstLimitRecord(t, steps)
	assert.Equal(t, limitReset.Add(time.Millisecond).Format(time.RFC3339Nano), record["wait_until"], "the wait ends one first backoff step after the wall clock")
	assert.Nil(t, record["reset_passed"], "an overload has no reset time that can pass")
}

func TestOverload_AnOverloadThatStaysPastTheCeilingFailsTheStepAndNamesTheLimit(t *testing.T) {
	t.Parallel()

	// The agent is overloaded for two runs more than the ceiling allows, then it works. A step that drops the ceiling ends with no error after those runs.
	h := limitedHarness(t, limitReset)
	inTurn(h, append(slices.Repeat([]agent.Result{overloaded()}, overloadWaits+2), exited)...)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "the agent step of item "+testItem+" stopped at the overload of login "+testLogin+", and neither the agent nor the usage read gives a reset time")
	require.ErrorContains(t, err, "Run beehest resume "+testItem+" when the limit is gone")
	assert.Len(t, h.agents.seen(), overloadWaits+1, "the first run and one rerun after each wait of the backoff")
	assert.Equal(t, overloadWaits, sleepsIn(recordsOfTestRun(t, engine)), "one sleep for each wait of the backoff")
}

func TestOverload_ARestartKeepsTheCountOfTheBackoffFromTheRecords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// hangAt is the agent start that the first engine holds while it stops. The overloads before it are recorded.
		hangAt int
	}{
		{"in_the_middle_of_the_backoff", 3},
		{"after_the_last_wait", overloadWaits + 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := limitedHarness(t, limitReset)
			h.agents.hangAt = tc.hangAt
			inTurn(h, append(slices.Repeat([]agent.Result{overloaded()}, overloadWaits+2), exited)...)

			first := h.engine(t)
			done := runInBackground(first, testItem)

			waitOn(t, waiting[struct{}]{what: "the agent start that the engine stops in", run: runOfEngine(t, first), read: func() (struct{}, bool, error) {
				return struct{}{}, len(h.agents.seen()) == tc.hangAt && h.agents.running() == 1, nil
			}})
			require.NoError(t, first.Shutdown())
			receive(t, done)

			second := h.engine(t)
			client := h.client(t, testVersion)

			var steps RunSteps

			waitOn(t, waiting[struct{}]{what: "the replayed run to fail at the ceiling", run: runOfEngine(t, second), read: func() (struct{}, bool, error) {
				var err error

				steps, err = client.Steps(testRun)

				return struct{}{}, err == nil && steps.State == runFailed, nil
			}})

			assert.Contains(t, steps.Error, "stopped at the overload of login "+testLogin)
			assert.Len(t, h.agents.seen(), overloadWaits+2, "the stopped start, and the replay starts each run from the first one that has no record")
			assert.Equal(t, overloadWaits, sleepsIn(recordsOfTestRun(t, second)), "the replay adds no wait to the backoff that the records hold")

			waits := map[string]string{}

			for _, step := range recordsOfTestRun(t, second) {
				if record, isText := step.Output.(string); isText && step.StepName != sleepStep {
					waits[step.StepName] = record
				}
			}

			for count := range overloadWaits {
				assert.Contains(t, waits["agent:fix#1/limit1."+strconv.Itoa(count)], limitReset.Add(time.Millisecond<<count).Format(time.RFC3339Nano), "wait number %d doubles from the count in the records", count)
			}
		})
	}
}

func TestOverload_TheBackoffDoublesAndAnotherLimitBetweenTwoOverloadsStartsItAgain(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, overloaded(), overloaded(), limited(limitReset.Add(-time.Hour), 0), overloaded(), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Len(t, h.agents.seen(), 5)

	waits := map[string]string{}

	for _, step := range recordsOfTestRun(t, engine) {
		record, isText := step.Output.(string)
		if isText && step.StepName != sleepStep {
			waits[step.StepName] = record
		}
	}

	assert.Contains(t, waits["agent:fix#1/limit1.0"], limitReset.Add(time.Millisecond).Format(time.RFC3339Nano), "the first overload waits one step")
	assert.Contains(t, waits["agent:fix#1/limit1.1"], limitReset.Add(2*time.Millisecond).Format(time.RFC3339Nano), "the second in a row waits two")
	assert.Contains(t, waits["agent:fix#1/limit1.3"], limitReset.Add(time.Millisecond).Format(time.RFC3339Nano), "an overload after a dated limit waits one step again")
}

func TestOverload_TheStatusOfAWaitingRunShowsTheOverloadAndTheNextTry(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	h.overload = time.Hour
	inTurn(h, overloaded(), exited)

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	var steps RunSteps

	waitOn(t, waiting[struct{}]{what: "the run to show the overload wait", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		var err error

		steps, err = client.Steps(testRun)

		return struct{}{}, err == nil && steps.LimitWait != nil, nil
	}})

	assert.Contains(t, steps.Notes(), "waits for the overload of login "+testLogin+" until "+limitReset.Add(time.Hour).Format(time.RFC3339))

	require.NoError(t, engine.Shutdown())
	receive(t, done)
}

func TestOverload_AResultThatNoWaitCuresFailsAtOnceWithNoWait(t *testing.T) {
	t.Parallel()

	denied := agent.Result{Outcome: agent.OutcomeExited, ExitCode: 1, Note: "the provider asks for usage credits, and no wait buys them: add usage credits or use another model"}
	h := limitedHarness(t, limitReset)
	inTurn(h, denied)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "usage credits")
	assert.Len(t, h.agents.seen(), 1, "no rerun")

	for _, step := range recordsOfTestRun(t, engine) {
		assert.NotContains(t, step.StepName, "/limit", "no limit step, so no wait")
	}
}
