package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// fixFlow is one fix lane.
const fixFlow = "def main(item):\n    agent(name = \"fix\", prompt = \"prompts/work.md\", model = \"m\")\n"

// limitReset is the time when the provider limit of the tests resets.
var limitReset = time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

// limited is a run that a rate limit stopped, as the runner reports it: the outcome limited, the limit, the session and the cost of the run.
func limited(reset time.Time, cost float64) agent.Result {
	return agent.Result{
		Outcome: agent.OutcomeLimited, ExitCode: 1, Session: "sess-limited", Note: "the provider refused more work at its rate limit five_hour",
		Limit: &agent.Limit{Kind: agent.LimitRate, Window: "five_hour", ResetsAt: reset}, Spend: agent.Spend{Cost: cost},
	}
}

// inTurn makes the fake agent end its runs with the results in order. The last result repeats.
func inTurn(h *harness, results ...agent.Result) {
	var calls atomic.Int32

	h.agents.result = func(AgentRequest) agent.Result {
		return results[min(int(calls.Add(1)), len(results))-1]
	}
}

// limitedHarness is a harness that runs one fix lane, with a wall clock that reads wall.
func limitedHarness(t *testing.T, wall time.Time) *harness {
	t.Helper()

	h := newHarness(t)
	h.flowIs(t, fixFlow)
	h.wall = func() time.Time { return wall }

	return h
}

// recordsOfTestRun reads the step records of the test run with their outputs.
func recordsOfTestRun(t *testing.T, engine *Engine) []dbos.StepInfo {
	t.Helper()

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun, dbos.WithStepsLoadOutput(true))
	require.NoError(t, err)

	return steps
}

// firstLimitRecord decodes the record of the first limited run of the fix lane from the step records.
func firstLimitRecord(t *testing.T, steps []dbos.StepInfo) map[string]any {
	t.Helper()

	var record map[string]any

	for _, step := range steps {
		if step.StepName == "agent:fix#1/limit1.0" {
			text, ok := step.Output.(string)
			require.True(t, ok, "the limit record is JSON text")
			require.NoError(t, json.Unmarshal([]byte(text), &record))
		}
	}

	require.NotNil(t, record, "the limited run left a limit record")

	return record
}

// limitedIn is a limited run that named the session, or none for an empty session.
func limitedIn(session string) agent.Result {
	result := limited(limitReset, 0)
	result.Session = session

	return result
}

// limitSentence is the first sentence of the note of a rerun after the limit of the tests.
const limitSentence = "This step ran before, and the rate limit five_hour of login " + testLogin + " stopped it."

// sleepsIn counts the durable sleeps among the step records.
func sleepsIn(steps []dbos.StepInfo) int {
	count := 0

	for _, step := range steps {
		if step.StepName == sleepStep {
			count++
		}
	}

	return count
}

func TestRateLimit_AFixLaneThatHitsARateLimitWaitsThenPassesAndCountsOneLane(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)
	assert.Len(t, h.agents.seen(), 2, "the limited run, then the rerun after the reset")

	count, err := (&script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}).countEarlier("fix")
	require.NoError(t, err)
	assert.Equal(t, 1, count, "the limited run and its rerun are one fix lane")
}

func TestRateLimit_ARestartDuringTheWaitSleepsTheRestAndStartsTheAgentOnceMore(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-1500*time.Millisecond))
	inTurn(h, limited(limitReset, 0), exited)

	first := h.engine(t)
	done := runInBackground(first, testItem)

	waitOn(t, inASleep(t, first))
	require.NoError(t, first.Shutdown())
	receive(t, done)
	require.Len(t, h.agents.seen(), 1, "the engine stopped in the wait")

	second := h.engine(t)
	client := h.client(t, testVersion)

	waitOn(t, waiting[struct{}]{what: "the replayed run to be done", run: runOfEngine(t, second), read: func() (struct{}, bool, error) {
		steps, err := client.Steps(testRun)

		return struct{}{}, err == nil && steps.State == runDone, nil
	}})

	assert.Len(t, h.agents.seen(), 2, "the replay reads the recorded phase and the limit, and starts the agent once after the reset")
	assert.Equal(t, 1, sleepsIn(recordsOfTestRun(t, second)), "the restart sleeps the rest of the one recorded sleep")
}

func TestRateLimit_TheWaitIsOneDurableSleepUntilTheRecordedReset(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0.5), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	steps := recordsOfTestRun(t, engine)
	assert.Equal(t, 1, sleepsIn(steps))

	record := firstLimitRecord(t, steps)
	assert.Equal(t, limitReset.Format(time.RFC3339), record["wait_until"])
	assert.Equal(t, map[string]any{"kind": "rate", "window": "five_hour", "resets_at": limitReset.Format(time.RFC3339)}, record["limit"])
	assert.Equal(t, testLogin, record["login"])
	assert.Equal(t, "sess-limited", record["session"])
	assert.InDelta(t, 0.5, record[costField], 0)
}

func TestRateLimit_AResetInThePastWaitsTheLeastTimeThenRunsAgain(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(time.Hour))
	inTurn(h, limited(limitReset, 0), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Len(t, h.agents.seen(), 2)
	assert.Equal(t, 1, sleepsIn(recordsOfTestRun(t, engine)), "one least wait before the rerun")
}

func TestRateLimit_ALimitThatNoReadingDatesFailsTheStepAndNamesTheLimit(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, limited(time.Time{}, 0))

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "the agent step of item "+testItem+" stopped at the rate limit five_hour of login "+testLogin+", and neither the agent nor the usage read gives a reset time")
	require.ErrorContains(t, err, "Run beehave resume "+testItem+" when the limit is gone")
	assert.Len(t, h.agents.seen(), 1, "no rerun without a time to wait for")

	steps, err := h.client(t, testVersion).Steps(testRun)
	require.NoError(t, err)

	for _, line := range steps.Steps {
		assert.Nil(t, line.LimitWait, "the failed line of %s waits for nothing", line.Name)
	}
}

func TestRateLimit_ALimitWithNoResetTimeWaitsForTheResetThatThePoolReads(t *testing.T) {
	t.Parallel()

	var asked []string

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	h.logins = fakeLogins{login: testLogin, reset: limitReset, asked: &asked}
	inTurn(h, limited(time.Time{}, 0), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Len(t, h.agents.seen(), 2, "the run that the limit stopped, then the rerun after the reset")
	assert.Equal(t, []string{testLogin}, asked, "the pool reads the usage of the login of the run once")

	steps := recordsOfTestRun(t, engine)
	assert.Equal(t, 1, sleepsIn(steps))

	record := firstLimitRecord(t, steps)
	assert.Equal(t, limitReset.Format(time.RFC3339), record["wait_until"], "the wait ends at the reset that the pool read gave")
	limit, isObject := record["limit"].(map[string]any) // the decoded JSON object of the limit
	require.True(t, isObject)
	assert.Equal(t, limitReset.Format(time.RFC3339), limit["resets_at"], "the recorded limit holds the read reset time, as if the agent gave it")
}

func TestRateLimit_TwoLimitsInARowWithAResetInThePastFailTheStep(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(time.Hour))
	inTurn(h, limited(limitReset, 0))

	_, err := h.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "the agent step of item "+testItem+" stopped at the rate limit five_hour of login "+testLogin+" twice in a row with a reset time that has passed")
	require.ErrorContains(t, err, "Run beehave resume "+testItem+" when the limit is gone")
	assert.Len(t, h.agents.seen(), 2, "one rerun after the first past reset, none after the second")
}

func TestRateLimit_AResumeAfterALimitWithNoResetTimeRunsTheAgentAgain(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, limited(time.Time{}, 0), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "gives a reset time")

	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))

	require.NoError(t, err, "the resume runs the agent again, and the limit is gone")
	assert.Len(t, h.agents.seen(), 2, "the limited run, then the run of the resume")
}

func TestRateLimit_ATimeThatThePoolReadsAndHasPassedTwiceInARowFailsTheStep(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(time.Hour))
	h.logins = fakeLogins{login: testLogin, reset: limitReset}
	inTurn(h, limited(time.Time{}, 0))

	_, err := h.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "stopped at the rate limit five_hour of login "+testLogin+" twice in a row with a reset time that has passed")
	assert.Len(t, h.agents.seen(), 2, "one rerun after the first past reset, none after the second")
}

func TestRateLimit_AFailedUsageReadFailsTheStepAndNamesTheLogin(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	h.logins = fakeLogins{login: testLogin, resetErr: errFake}
	inTurn(h, limited(time.Time{}, 0))

	_, err := h.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "read the usage of login "+testLogin+" for the reset of the rate limit five_hour: "+errFake.Error())
	require.ErrorContains(t, err, "Run beehave resume "+testItem+" when the usage read works")
	assert.Len(t, h.agents.seen(), 1, "no rerun after a failed read")
}

func TestRateLimit_TheDryRunPoolKnowsNoResetTime(t *testing.T) {
	t.Parallel()

	_, known, err := dryLogins{}.Reset(t.Context(), testProject, testLogin)

	require.NoError(t, err)
	assert.False(t, known)
}

func TestRateLimit_ALimitWithAResetTimeNeverAsksThePool(t *testing.T) {
	t.Parallel()

	var asked []string

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	h.logins = fakeLogins{login: testLogin, reset: limitReset.Add(time.Hour), asked: &asked}
	inTurn(h, limited(limitReset, 0), exited)

	_, err := h.engine(t).Run(testItem, testProject)

	require.NoError(t, err)
	assert.Empty(t, asked, "the reset time of the agent stands")
}

func TestRateLimit_TwoLimitedRunsOfOneTryKeepOneRecordEachNumberedFromZero(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0), limited(limitReset, 0), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	var names []string

	for _, step := range recordsOfTestRun(t, engine) {
		if strings.HasPrefix(step.StepName, "agent:fix#1/limit1.") {
			names = append(names, step.StepName)
		}
	}

	assert.Equal(t, []string{"agent:fix#1/limit1.0", "agent:fix#1/limit1.1"}, names, "each limited run of the try has its own record, numbered from 0")
}

func TestRateLimit_AResumeAfterTwoPastResetsInARowRunsTheAgentAgain(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(time.Hour))
	inTurn(h, limited(limitReset, 0), limited(limitReset, 0), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "twice in a row")

	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))

	require.NoError(t, err, "the resume runs the agent again, and the limit is gone")
	assert.Len(t, h.agents.seen(), 3, "two limited runs, then the run of the resume")
}

func TestRateLimit_TheCostOfARunThatFailedAtALimitCountsOnce(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, limited(time.Time{}, 0.7))

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "gives a reset time")

	client := h.client(t, testVersion)

	steps, err := client.Steps(testRun)
	require.NoError(t, err)
	assert.InDelta(t, 0.7, steps.Spent.Cost, 1e-9, "the status view")

	tree, err := runAndBelowSpend(engine.ctx, testRun)
	require.NoError(t, err)
	assert.InDelta(t, 0.7, tree.Cost, 1e-9, "the sum that the tree cap reads")

	days, err := daysFromTheRun(t, client)
	require.NoError(t, err)
	assert.InDelta(t, 0.7, totalOfDays(days).Spent.Cost, 1e-9, "the per-day sum")
}

func TestRateLimit_TheTreeSumCountsTheFailedAgentRunOfAChildRun(t *testing.T) {
	t.Parallel()

	// The agent step a of the root costs 0.3. The agent step x of the child run fails at a limit with no reset time after 0.7.
	rig := newAgentRig(t)
	inTurn(rig.h, costing(0.3), limited(time.Time{}, 0.7))

	engine := rig.h.engine(t)

	_, err := engine.RunFile(testItem, testProject, capFile(t, treeFlow), modeOff())
	require.NoError(t, err, "the root run ends: the gather reports the child run that failed")

	child, err := runAndBelowSpend(engine.ctx, childRun)
	require.NoError(t, err)
	assert.InDelta(t, 0.7, child.Cost, 1e-9, "the child run holds the cost of its failed agent run")

	tree, err := runAndBelowSpend(engine.ctx, testRun)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, tree.Cost, 1e-9, "the tree holds the root step and the failed agent run of the child run")
}

func TestRateLimit_AResumeKeepsTheCostOfTheFailedRunInEveryTotal(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, limited(time.Time{}, 0.7), costing(0.3))

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "neither the agent nor the usage read gives a reset time")

	client := h.client(t, testVersion)
	_, err = resumeAndJoin(t, engine, client)
	require.NoError(t, err, "the rerun of the resume ends well")

	assertTotals(t, engine, client, 1.0)
}

// assertTotals checks the cost of the test run in the status view, the list of runs, the sum that the tree cap reads and the per-day sum.
func assertTotals(t *testing.T, engine *Engine, client *Client, want float64) {
	t.Helper()

	steps, err := client.Steps(testRun)
	require.NoError(t, err)
	assert.InDelta(t, want, steps.Spent.Cost, 1e-9, "the status view")

	runs, err := client.Status()
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.InDelta(t, want, runs[0].Spent.Cost, 1e-9, "the list of runs")

	tree, err := runAndBelowSpend(engine.ctx, testRun)
	require.NoError(t, err)
	assert.InDelta(t, want, tree.Cost, 1e-9, "the sum that the tree cap reads")

	days, err := daysFromTheRun(t, client)
	require.NoError(t, err)
	assert.InDelta(t, want, totalOfDays(days).Spent.Cost, 1e-9, "the per-day sum")
}

func TestRateLimit_AResumeKeepsTheLimitRecordOfAnEarlierRun(t *testing.T) {
	t.Parallel()

	// Run 0 of the try is limited with a reset time, run 1 fails at a limit with none, and the rerun of the resume ends well.
	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0.2), limited(time.Time{}, 0.7), costing(0.3))

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "neither the agent nor the usage read gives a reset time")

	client := h.client(t, testVersion)
	_, err = resumeAndJoin(t, engine, client)
	require.NoError(t, err, "the rerun of the resume ends well")

	assertTotals(t, engine, client, 1.2)
}

func TestRateLimit_TwoResumesKeepTheCostOfBothFailedRuns(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, limited(time.Time{}, 0.7), limited(time.Time{}, 0.4), costing(0.3))

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "neither the agent nor the usage read gives a reset time")

	client := h.client(t, testVersion)
	_, err = resumeAndJoin(t, engine, client)
	require.ErrorContains(t, err, "neither the agent nor the usage read gives a reset time", "the rerun of the first resume fails at the limit again")

	_, err = resumeAndJoin(t, engine, client)
	require.NoError(t, err, "the rerun of the second resume ends well")

	assertTotals(t, engine, client, 1.4)
}

func TestRateLimit_AStoreThatRefusesTheCostOfAFailedRunKeepsTheErrorOfTheRun(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, limited(time.Time{}, 0.7))

	engine := h.engine(t)

	_, err := sqlOf(t, h).ExecContext(t.Context(), "CREATE TRIGGER refuse_failed_run BEFORE INSERT ON streams BEGIN SELECT RAISE(ABORT, 'the store refused the cost'); END")
	require.NoError(t, err)

	_, err = engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "neither the agent nor the usage read gives a reset time", "the error of the failed run is the error of the step")
	assert.NotContains(t, err.Error(), "the store refused the cost")
}

func TestRateLimit_AStoreThatRefusesTheLimitRecordAndTheCostKeepsTheErrorOfTheLimitStep(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0.7), exited)

	engine := h.engine(t)

	_, err := sqlOf(t, h).ExecContext(t.Context(), "CREATE TRIGGER refuse_limit_and_cost BEFORE INSERT ON operation_outputs "+
		"WHEN NEW.function_name IN ('agent:fix#1/limit1.0', 'agent:fix#1/spent1') BEGIN SELECT RAISE(ABORT, 'the store refused the record'); END")
	require.NoError(t, err)

	_, err = engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "the store refused the record")
	assert.Len(t, h.agents.seen(), 1, "no rerun after a failed limit record")
}

func TestClient_AStreamOfFailedRunsThatCannotBeReadFailsEveryTotal(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	_, err = sqlOf(t, h).ExecContext(t.Context(), "ALTER TABLE streams RENAME TO streams_gone")
	require.NoError(t, err)

	client := h.client(t, testVersion)

	_, err = client.Steps(testRun)
	require.ErrorContains(t, err, "read the cost of the failed agent runs of "+testRun, "the status view")

	_, err = client.Status()
	require.ErrorContains(t, err, "read the cost of the failed agent runs of "+testRun, "the list of runs")

	_, err = runSpend(engine.ctx, testRun)
	require.ErrorContains(t, err, "read the cost of the failed agent runs of "+testRun, "the sum that the caps read")

	_, err = runAndBelowSpend(engine.ctx, testRun)
	require.ErrorContains(t, err, "read the cost of the failed agent runs of "+testRun, "the sum of the tree cap")

	_, err = daysFromTheRun(t, client)
	require.ErrorContains(t, err, "read the cost of the failed agent runs of "+testRun, "the per-day sum")
}

func TestBudget_AResumeKeepsTheCostOfTheFailedRunInTheRunCap(t *testing.T) {
	t.Parallel()

	// Step a fails at a limit after $0.70 under a run cap of $0.50. The resume rewinds the phase of a to its look, which counts the failed run and stops a.
	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 0.5
	inTurn(rig.h, limited(time.Time{}, 0.7), costing(0.3))

	file := capFile(t, bodyFlow(`agent(model = "m", prompt = "prompts/a.md", name = "a")
agent(model = "m", prompt = "prompts/a.md", name = "b")
`))

	engine := rig.h.engine(t)

	_, err := engine.RunFile(testItem, testProject, file, modeOff())
	require.ErrorContains(t, err, "neither the agent nor the usage read gives a reset time")

	client := rig.h.client(t, testVersion)
	_ = resumeRun(t, engine, client)

	waitOn(t, condition(func() bool { return stoppedRun(t, client, testRun) != nil || len(rig.h.agents.seen()) > 1 }))

	stop := stoppedRun(t, client, testRun)
	require.NotNil(t, stop, "the look of the rerun of a stops it at the run cap")
	assert.Equal(t, scopeRun, stop.Scope)
	assert.InDelta(t, 0.7, stop.Spent, 1e-9, "the look counts the cost of the failed run")
	assert.Len(t, rig.h.agents.seen(), 1, "the rerun of a does not call the agent past the cap")
}

func TestRateLimit_AStoreThatRefusesTheLimitRecordFailsTheStepAndTheCostOfTheLimitedRunCounts(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0.7), exited)

	engine := h.engine(t)

	_, err := sqlOf(t, h).ExecContext(t.Context(), "CREATE TRIGGER refuse_limit_record BEFORE INSERT ON operation_outputs "+
		"WHEN NEW.function_name = 'agent:fix#1/limit1.0' BEGIN SELECT RAISE(ABORT, 'the store refused the record'); END")
	require.NoError(t, err)

	_, err = engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "the store refused the record")
	assert.Len(t, h.agents.seen(), 1, "no rerun after a failed limit record")

	tree, err := runAndBelowSpend(engine.ctx, testRun)
	require.NoError(t, err)
	assert.InDelta(t, 0.7, tree.Cost, 1e-9, "the cost of the limited run is recorded by the step of the failed run")
}

// liftRefusal drops the trigger of refuseOutcome, so the store writes every record again.
func liftRefusal(t *testing.T, h *harness) {
	t.Helper()

	_, err := sqlOf(t, h).ExecContext(t.Context(), "DROP TRIGGER refuse_outcome")
	require.NoError(t, err)
}

func TestRateLimit_AResumeAfterTheStoreRefusedTheLimitRecordRunsTheAgentAgainAndCountsTheCostOnce(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0.2), exited)

	engine := h.engine(t)
	refuseOutcome(t, sqlOf(t, h), "agent:fix#1/limit1.0")

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, refusedText)

	var failure *dbos.StepInfo

	for _, step := range recordsOfTestRun(t, engine) {
		if step.StepName == "agent:fix#1/limit1.0" {
			failure = &step
		}
	}

	require.NotNil(t, failure, "the limit step has a record")
	require.Error(t, failure.Error, "the record of the limit step holds a failure")
	assert.Contains(t, failure.Error.Error(), "Step agent:fix#1/limit1.0 in workflow", "the failure is the error of the limit step")
	assert.Contains(t, failure.Error.Error(), refusedText, "the failure is the refusal of the limit record")
	liftRefusal(t, h)

	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))

	require.NoError(t, err, "the resume cuts the history at the failure of the limit step, and the replay records the limit")
	assert.Len(t, h.agents.seen(), 2, "the limited run, then the run of the resume")

	tree, err := runAndBelowSpend(engine.ctx, testRun)
	require.NoError(t, err)
	assert.InDelta(t, 0.2, tree.Cost, 1e-9, "the resume records the cost of the limited run once")
}

func TestRateLimit_AResumeAfterTheStoreRefusedTheLimitRecordOfASecondRunCountsEachRunOnce(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0.2), limited(limitReset, 0.7), exited)

	engine := h.engine(t)
	refuseOutcome(t, sqlOf(t, h), "agent:fix#1/limit1.1")

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, refusedText)
	liftRefusal(t, h)

	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))

	require.NoError(t, err, "the resume runs the second limited run again, and the limit is gone")
	assert.Len(t, h.agents.seen(), 3, "two limited runs, then the run of the resume")

	tree, err := runAndBelowSpend(engine.ctx, testRun)
	require.NoError(t, err)
	assert.InDelta(t, 0.9, tree.Cost, 1e-9, "each limited run counts once")
}

func TestRateLimit_TheSleepLastsUntilTheRecordedReset(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-time.Hour))
	inTurn(h, limited(limitReset, 0), exited)

	engine := h.engine(t)
	done := runInBackground(engine, testItem)

	waitOn(t, inASleep(t, engine))

	var sleep dbos.StepInfo

	for _, step := range recordsOfTestRun(t, engine) {
		if step.StepName == sleepStep {
			sleep = step
		}
	}

	// The library records the end of the sleep as the end of its step.
	assert.InDelta(t, time.Hour.Seconds(), sleep.CompletedAt.Sub(sleep.StartedAt).Seconds(), time.Minute.Seconds(), "the sleep ends at the reset, one hour after the wall clock")

	require.NoError(t, engine.Shutdown())
	receive(t, done)
}

func TestRateLimit_TheCostOfTheLimitedRunCountsInTheRunCapAndTheTreeSum(t *testing.T) {
	t.Parallel()

	for _, scope := range []string{scopeRun, scopeTree} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)
			rig.h.wall = func() time.Time { return limitReset.Add(-30 * time.Millisecond) }

			if scope == scopeRun {
				rig.h.opts.MaxRunBudget = 5
			} else {
				rig.h.opts.MaxTreeBudget = 5
			}

			inTurn(rig.h, limited(limitReset, 6), exited)

			stop := holdOnCap(t, rig, capFile(t, bodyFlow(`agent(model = "m", prompt = "prompts/a.md", name = "a")`+"\n")))

			require.NotNil(t, stop)
			assert.Equal(t, scope, stop.Scope)
			assert.InDelta(t, 6.0, stop.Spent, 0, "the cost of the limited run is in the sum")
			assert.Len(t, rig.h.agents.seen(), 1, "the rerun waits at the cap")
		})
	}
}

func TestRateLimit_ALimitedRunUsesNoOutputTry(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.wall = func() time.Time { return limitReset.Add(-30 * time.Millisecond) }

	var calls atomic.Int32

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		if calls.Add(1) == 1 {
			return limited(limitReset, 0)
		}

		rig.put(outputPath(request.Prompt), `{"verdict": "ok"}`)

		return exited
	}

	flow := bodyFlow("a = " + agentCallText(`outputs = ["verdict"], output_tries = 1, `) + `command(run = ["show", a.verdict])` + "\n")

	_, err := rig.run(t, flow, "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"show ok"}, rig.h.commands.ran())
	assert.Len(t, rig.h.agents.seen(), 2)
}

func TestRateLimit_TheStatusOfAWaitingRunNamesTheLoginAndTheReset(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-time.Hour))
	inTurn(h, limited(limitReset, 0), exited)

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	var steps RunSteps

	waitOn(t, waiting[struct{}]{what: "the run to show the rate limit wait", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		var err error

		steps, err = client.Steps(testRun)

		return struct{}{}, err == nil && steps.LimitWait != nil, nil
	}})

	note := "waits for the rate limit five_hour of login " + testLogin + " until " + limitReset.Format(time.RFC3339)
	assert.Contains(t, steps.Notes(), note)
	assert.Equal(t, runRunning, steps.State, "the run stays open while it waits")

	var line StepLine

	for _, step := range steps.Steps {
		if step.Name == "agent:fix#1" {
			line = step
		}
	}

	require.NotNil(t, line.LimitWait, "the line of the waiting step holds the limit")
	assert.Equal(t, LimitWait{Limit: *limited(limitReset, 0).Limit, Login: testLogin, WaitUntil: limitReset}, *line.LimitWait)

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Contains(t, infos[0].Notes(), note, "the list of runs shows the wait too")

	require.NoError(t, engine.Shutdown())
	receive(t, done)
}

func TestRateLimit_ThePhaseThatStartsAfterALimitEndsItsWaitOnTheLine(t *testing.T) {
	t.Parallel()

	record, err := json.Marshal(limitRecord{LimitWait: LimitWait{Limit: *limited(limitReset, 0).Limit, Login: testLogin, WaitUntil: limitReset}, Spend: agent.Spend{Cost: 2}})
	require.NoError(t, err)

	var line StepLine

	line.absorb(dbos.StepInfo{StepName: "agent:a#1/limit1.0", Output: string(record)})
	require.NotNil(t, line.LimitWait)
	assert.InDelta(t, 2.0, line.Cost, 0)

	// The rerun is a new phase that has not ended, so its record holds no output yet.
	line.absorb(dbos.StepInfo{StepName: phaseName, ChildWorkflowID: "phase-2"})
	assert.Nil(t, line.LimitWait, "the rerun has started: the step no longer waits")
	assert.InDelta(t, 2.0, line.Cost, 0, "the cost of the limited run stays on the line")
}

func TestCapStop_TheLineKeepsTheStopUntilAPhaseEndsWhoseLookPassed(t *testing.T) {
	t.Parallel()

	stopped, err := json.Marshal(PhaseOutput{CapStop: &CapStop{Scope: scopeRun, Cap: 0.5, Spent: 0.7}})
	require.NoError(t, err)

	passed, err := json.Marshal(PhaseOutput{Outcome: agent.OutcomeExited})
	require.NoError(t, err)

	var line StepLine

	line.absorb(dbos.StepInfo{StepName: phaseName, ChildWorkflowID: "phase-1", Output: string(stopped)})
	require.NotNil(t, line.CapStop, "the phase whose look stopped shows the stop")

	line.absorb(dbos.StepInfo{StepName: phaseName, ChildWorkflowID: "phase-2"})
	require.NotNil(t, line.CapStop, "a phase that has not ended may stop at its look again")

	line.absorb(dbos.StepInfo{StepName: phaseName, ChildWorkflowID: "phase-2", Output: string(passed)})
	assert.Nil(t, line.CapStop, "a phase that ran the agent passed its look")
}

func TestRateLimit_ALimitRecordThatCannotBeReadMarksTheCostUnknown(t *testing.T) {
	t.Parallel()

	var line StepLine

	assert.False(t, line.decodeAgentSub("limit1.0", "not json"))
	assert.True(t, line.Unknown, "a cost that cannot be read is never 0 for the caps")
	assert.True(t, line.Unreadable)
	assert.Nil(t, line.LimitWait)
}

func TestRateLimit_ALimitedRunWithNoLimitFailsThePhase(t *testing.T) {
	t.Parallel()

	err := phaseEnd(PhaseInput{Item: testItem}, PhaseOutput{Outcome: agent.OutcomeLimited, ExitCode: 1, Note: "no limit"}, limitReset)

	require.ErrorContains(t, err, "ended as limited (exit 1): no limit")
	require.NoError(t, phaseEnd(PhaseInput{Item: testItem}, PhaseOutput{Outcome: agent.OutcomeLimited, Limit: limited(limitReset, 0).Limit}, limitReset.Add(-time.Hour)))
}

func TestRateLimit_AStoredPhaseWithoutALimitDecodesAsBefore(t *testing.T) {
	t.Parallel()

	var stored PhaseOutput

	require.NoError(t, json.Unmarshal([]byte(`{"outcome":"exited","exit_code":0,"note":"","session":"s","cost_usd":1}`), &stored))
	assert.Nil(t, stored.Limit, "a phase that agent version 1 recorded has no limit")

	plain, err := json.Marshal(PhaseOutput{Outcome: agent.OutcomeExited})
	require.NoError(t, err)
	assert.NotContains(t, string(plain), `"limit"`, "a phase with no limit keeps the form of version 1")

	withLimit, err := json.Marshal(PhaseOutput{Outcome: agent.OutcomeLimited, Limit: limited(limitReset, 0).Limit})
	require.NoError(t, err)
	assert.Contains(t, string(withLimit), `"limit":{"kind":"rate","window":"five_hour","resets_at":"2026-11-01T05:00:00Z"}`)
}

func TestRateLimit_TheRerunResumesTheSessionOfTheLimitedRun(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0), exited)

	_, err := h.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := h.agents.seen()
	require.Len(t, requests, 2)
	assert.Nil(t, requests[0].Lost, "the first run follows no limit")
	require.NotNil(t, requests[1].Lost, "the rerun resumes the session of the limited run")
	assert.Equal(t, "sess-limited", requests[1].Lost.Session)
	assert.Contains(t, requests[1].Lost.Continue, "The rate limit five_hour of login "+testLogin+" stopped you")
	assert.Contains(t, requests[1].Lost.Continue, "Continue the step from where you stopped.")
	assert.Contains(t, requests[1].Lost.Note, limitSentence)
	assert.Contains(t, requests[1].Lost.Note, "Read `git status`")
	assert.Equal(t, "Work on the item.\n", requests[1].Prompt, "the note goes with the limited run, not into the prompt")
}

func TestRateLimit_ARerunAfterALimitedRunWithNoSessionGetsTheNoteAndTheDiffSummaryInThePrompt(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))

	var calls atomic.Int32

	h.agents.result = func(AgentRequest) agent.Result {
		if calls.Add(1) > 1 {
			return exited
		}

		// The limited agent committed one file before the limit stopped it.
		commitIn(t, h.trees.dir, "done.txt")

		return limitedIn("")
	}

	_, err := h.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := h.agents.seen()
	require.Len(t, requests, 2)
	assert.Nil(t, requests[1].Lost, "a limited run that named no session has no session to resume")
	assert.Contains(t, requests[1].Prompt, limitSentence)
	assert.Contains(t, requests[1].Prompt, "done.txt")
	assert.Contains(t, requests[1].Prompt, "1 file changed")
}

func TestRateLimit_ARestartDuringTheWaitResumesTheSameSessionWithTheRecordedNote(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-1500*time.Millisecond))
	inTurn(h, limited(limitReset, 0), exited)

	first := h.engine(t)
	done := runInBackground(first, testItem)

	waitOn(t, inASleep(t, first))
	require.NoError(t, first.Shutdown())
	receive(t, done)
	require.Len(t, h.agents.seen(), 1, "the engine stopped in the wait")

	second := h.engine(t)
	client := h.client(t, testVersion)

	waitOn(t, waiting[struct{}]{what: "the replayed run to be done", run: runOfEngine(t, second), read: func() (struct{}, bool, error) {
		steps, err := client.Steps(testRun)

		return struct{}{}, err == nil && steps.State == runDone, nil
	}})

	record := firstLimitRecord(t, recordsOfTestRun(t, second))
	note, ok := record["note"].(string)
	require.True(t, ok, "the limit record holds the note")

	requests := h.agents.seen()
	require.Len(t, requests, 2)
	require.NotNil(t, requests[1].Lost, "the replay builds the rerun from the limit record")
	assert.Equal(t, "sess-limited", requests[1].Lost.Session)
	assert.Equal(t, note, requests[1].Lost.Note, "the rerun gets the note that the limit step recorded")
}

func TestRateLimit_ACommitDuringTheWaitStaysOutOfTheRecordedNoteOfTheRerun(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-1500*time.Millisecond))
	inTurn(h, limited(limitReset, 0), exited)

	first := h.engine(t)
	done := runInBackground(first, testItem)

	waitOn(t, inASleep(t, first))
	require.NoError(t, first.Shutdown())
	receive(t, done)
	require.Len(t, h.agents.seen(), 1, "the engine stopped in the wait")

	// A commit lands in the item folder after the limit step recorded the note.
	commitIn(t, h.trees.dir, "late.txt")

	second := h.engine(t)
	client := h.client(t, testVersion)

	waitOn(t, waiting[struct{}]{what: "the replayed run to be done", run: runOfEngine(t, second), read: func() (struct{}, bool, error) {
		steps, err := client.Steps(testRun)

		return struct{}{}, err == nil && steps.State == runDone, nil
	}})

	requests := h.agents.seen()
	require.Len(t, requests, 2)
	require.NotNil(t, requests[1].Lost)
	assert.Contains(t, requests[1].Lost.Note, limitSentence)
	assert.NotContains(t, requests[1].Lost.Note, "late.txt", "the replay reads the note that the limit step recorded and reads no new diff")
}

func TestRateLimit_ASecondLimitResumesTheSessionOfTheSecondRunWithThePromptOfTheTry(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limitedIn("S1"), limitedIn("S2"), exited)

	_, err := h.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := h.agents.seen()
	require.Len(t, requests, 3)
	require.NotNil(t, requests[1].Lost)
	assert.Equal(t, "S1", requests[1].Lost.Session)
	require.NotNil(t, requests[2].Lost)
	assert.Equal(t, "S2", requests[2].Lost.Session, "the second rerun resumes the session of the second limited run")
	assert.Equal(t, requests[1].Lost.Note, requests[2].Lost.Note, "each rerun gets one note")

	for i, request := range requests {
		assert.Equal(t, "Work on the item.\n", request.Prompt, "the prompt of run %d", i)
	}
}

func TestRateLimit_ASecondLimitedRunThatNamedNoSessionResumesTheFirstSession(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0), limitedIn(""), exited)

	_, err := h.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := h.agents.seen()
	require.Len(t, requests, 3)
	require.NotNil(t, requests[2].Lost, "the second limited run named no session, so the first one is still the session of the step")
	assert.Equal(t, "sess-limited", requests[2].Lost.Session)
	assert.Equal(t, "Work on the item.\n", requests[2].Prompt)
}

func TestRateLimit_TwoLimitedRunsThatNamedNoSessionPutOneNoteInThePrompt(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limitedIn(""), limitedIn(""), exited)

	_, err := h.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := h.agents.seen()
	require.Len(t, requests, 3)
	assert.Nil(t, requests[2].Lost)
	assert.Equal(t, 1, strings.Count(requests[2].Prompt, limitSentence), "each rerun starts from the input of the try: %q", requests[2].Prompt)
}

func TestRateLimit_AResumeAfterAFailedRerunResumesTheLimitedSession(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0), agent.Result{Outcome: agent.OutcomeExited, ExitCode: 3, Note: "bad exit"}, exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "(exit 3)")

	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))
	require.NoError(t, err)

	requests := h.agents.seen()
	require.Len(t, requests, 3)
	require.NotNil(t, requests[2].Lost, "the reopen keeps the input of the rerun, which names the limited session")
	assert.Equal(t, "sess-limited", requests[2].Lost.Session)
}

func TestRateLimit_ALimitedRunWhoseDiffCannotBeReadStillResumesItsSession(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))

	var calls atomic.Int32

	h.agents.result = func(AgentRequest) agent.Result {
		if calls.Add(1) > 1 {
			// read-tree writes a new index from HEAD and does not read the broken one.
			gitIn(t, h.trees.dir, "read-tree", "HEAD")

			return exited
		}

		// The limited agent committed one file and left a broken index, so git refuses the diff.
		commitIn(t, h.trees.dir, "done.txt")
		require.NoError(t, os.WriteFile(filepath.Join(h.trees.dir, ".git", "index"), []byte("not an index"), 0o600))

		return limited(limitReset, 0)
	}

	_, err := h.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := h.agents.seen()
	require.Len(t, requests, 2)
	require.NotNil(t, requests[1].Lost, "the diff serves only the note, so its failure keeps the resume")
	assert.Equal(t, "sess-limited", requests[1].Lost.Session)
	assert.Contains(t, requests[1].Lost.Note, "The engine could not read the diff summary of the changes since the step started.")

	log, err := os.ReadFile(filepath.Join(h.opts.StateDir, logFileName))
	require.NoError(t, err)
	assert.Contains(t, string(log), "cannot read the diff summary of the limited agent run", "the log holds the reason")
}

func TestRateLimit_ALimitRecordWithoutANoteDecodesAndAPhaseWithoutLimitedDecodesAsBefore(t *testing.T) {
	t.Parallel()

	old := `{"limit":{"kind":"rate","window":"five_hour","resets_at":"2026-11-01T05:00:00Z"},"login":"alice","wait_until":"2026-11-01T05:00:00Z","session":"s","cost_usd":0.5}`

	var line StepLine

	require.True(t, line.fromLimit(old), "a limit record with no note decodes for the status view")
	require.NotNil(t, line.LimitWait)
	assert.Equal(t, limitReset, line.LimitWait.WaitUntil.UTC())
	assert.InDelta(t, 0.5, line.Cost, 0)

	var in PhaseInput

	require.NoError(t, json.Unmarshal([]byte(`{"run":"r","item":"i","project":"p","login":"alice"}`), &in))
	assert.Nil(t, in.Limited, "a stored phase with no limited field decodes with none")

	plain, err := json.Marshal(PhaseInput{Item: "i"})
	require.NoError(t, err)
	assert.NotContains(t, string(plain), `"limited"`, "a phase with no limited run keeps its form")

	rerun, err := json.Marshal(PhaseInput{Item: "i", Limited: &LostRun{Session: "s", Note: "n", Continue: "c"}})
	require.NoError(t, err)
	assert.Contains(t, string(rerun), `"limited":{"session":"s","note":"n","continue":"c"}`)

	out, err := json.Marshal(PhaseOutput{Outcome: agent.OutcomeExited})
	require.NoError(t, err)
	assert.NotContains(t, string(out), `"resume_of"`, "a run that was given no session to resume keeps the form of its phase output")
}

func TestLimitNote_NamesTheLimitAndTheLoginAndQuotesTheDiffSummary(t *testing.T) {
	t.Parallel()

	stat := " a.txt | 1 +\n 1 file changed, 1 insertion(+)"
	record := limitRecord{LimitWait: LimitWait{Limit: *limited(limitReset, 0).Limit, Login: testLogin}}

	note := record.note(viewDiff, stat)
	_, lostTail, found := strings.Cut(LostStep{DiffStat: stat}.note(viewDiff), ". ")
	require.True(t, found)

	assert.Equal(t, limitSentence+" "+lostTail, note, "the note names the limit and the login, then shares the part about the work tree with the note of a lost step")
	assert.Contains(t, note, stat)
	assert.Equal(t, "The rate limit five_hour of login "+testLogin+" stopped you while you worked on this step, and it has reset. "+
		"Continue the step from where you stopped. Your brief and the output file are earlier in this session.", record.continued())
}
