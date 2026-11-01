package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capRefusal is the refusal of a pool with no runner under the usage caps.
type capRefusal struct {
	text  string
	reset time.Time
}

func (r *capRefusal) Error() string { return r.text }

func (r *capRefusal) ResetAt() (time.Time, bool) { return r.reset, !r.reset.IsZero() }

// capLogins refuses every pick that does not allow the cap to be passed, until it has refused `refusals` times. Then it picks alice.
type capLogins struct {
	mu       sync.Mutex
	refusals int
	refusal  *capRefusal
	calls    int
	over     []bool
}

func (l *capLogins) Pick(_ context.Context, _, _ string, _ []string, overCap bool) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.calls++
	l.over = append(l.over, overCap)

	if overCap {
		return "alice", nil
	}

	if l.calls <= l.refusals {
		return "", l.refusal
	}

	return "alice", nil
}

func (l *capLogins) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.calls
}

func TestLoginStep_WaitsForTheEarliestResetThenPicksAgain(t *testing.T) {
	t.Parallel()

	reset := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	harness := newHarness(t)
	harness.wall = func() time.Time { return reset.Add(-30 * time.Millisecond) }
	logins := &capLogins{refusals: 2, refusal: &capRefusal{text: "no claude runner is under both caps", reset: reset}}
	harness.picker = logins

	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Equal(t, 3, logins.count(), "two refusals, each followed by a durable sleep, then the pick")
	assert.Equal(t, []string{"alice"}, harness.agents.logins())

	steps, err := dbos.GetWorkflowSteps(engine.ctx, runID(testItem, 1))
	require.NoError(t, err)

	var sleeps int

	for _, step := range steps {
		if step.StepName == "DBOS.sleep" {
			sleeps++
		}
	}

	assert.Equal(t, 2, sleeps)
}

func TestLoginStep_AResetTimeInThePastWaitsTheLeastTimeBeforeTheNextRead(t *testing.T) {
	t.Parallel()

	reset := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	harness := newHarness(t)
	harness.wall = func() time.Time { return reset.Add(time.Hour) }
	logins := &capLogins{refusals: 1, refusal: &capRefusal{text: "no claude runner is under both caps", reset: reset}}
	harness.picker = logins

	_, err := harness.engine(t).Run(testItem, testProject)

	require.NoError(t, err)
	assert.Equal(t, 2, logins.count())
}

func TestLoginStep_ACanceledRunEndsTheWaitForAReset(t *testing.T) {
	t.Parallel()

	reset := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	harness := newHarness(t)
	harness.wall = func() time.Time { return reset.Add(-time.Hour) }
	logins := &capLogins{refusals: 100, refusal: &capRefusal{text: "no claude runner is under both caps", reset: reset}}
	harness.picker = logins
	engine := harness.engine(t)
	done := runInBackground(engine, testItem)

	waitForTheSleep(t, engine)
	require.NoError(t, harness.client(t, testVersion).Cancel(testItem))
	harness.tick(t)

	outcome := <-done

	require.Error(t, outcome.err)
	assert.Equal(t, 1, logins.count(), "the run ends in the wait and reads no more")
	assert.Empty(t, harness.agents.seen())
}

func TestLoginStep_FailsWithTheRefusalWhenNoResetIsKnown(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	logins := &capLogins{refusals: 100, refusal: &capRefusal{text: "no claude runner is under both caps. No reset time is known."}}
	harness.picker = logins

	_, err := harness.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "pick the login of item "+testItem)
	require.ErrorContains(t, err, "no claude runner is under both caps. No reset time is known.")
	assert.Equal(t, 1, logins.count(), "no wait and no second read")
	assert.Empty(t, harness.agents.seen())
}

func TestLoginStep_ARunnerNamedByTheStepNeverAsksThePool(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	logins := &capLogins{refusals: 100, refusal: &capRefusal{text: "no runner"}}
	harness.picker = logins
	harness.flowIs(t, "def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\", runner = \"named\")\n")

	_, err := harness.engine(t).Run(testItem, testProject)

	require.NoError(t, err)
	assert.Zero(t, logins.count())
	assert.Equal(t, []string{"named"}, harness.agents.logins())
}

func TestLoginStep_OverCapTakesARunnerAtOnceAndTheRecordSaysSo(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.OverCap = true
	logins := &capLogins{refusals: 100, refusal: &capRefusal{text: "no claude runner is under both caps"}}
	harness.picker = logins
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Equal(t, []bool{false, true}, logins.over, "the pick under the caps comes first, then the pick over them")
	assert.Equal(t, []string{"alice"}, harness.agents.logins())

	steps, err := dbos.GetWorkflowSteps(engine.ctx, runID(testItem, 1), dbos.WithStepsLoadOutput(true))
	require.NoError(t, err)

	var recorded string

	for _, step := range steps {
		if step.StepName == "login#1" {
			text, ok := step.Output.(string)
			require.True(t, ok)

			recorded = text
		}
	}

	assert.JSONEq(t, `{"login":"alice","over_cap":true,"reset":"0001-01-01T00:00:00Z"}`, recorded)
}

func TestLoginStep_OverCapLeavesAPickUnderTheCapsAlone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.OverCap = true
	logins := &capLogins{}
	harness.picker = logins
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Equal(t, []bool{false}, logins.over, "a runner under the caps is taken as it is")
}
