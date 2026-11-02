package flow

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const timedWaitFlow = "def main(item):\n    r = wait(name = \"ask\", question = \"What now?\", timeout = \"%s\")\n    if r.timed_out:\n        fail(\"TIMED OUT\")\n    if r.answer != \"go on\":\n        fail(\"WRONG ANSWER\")\n"

func TestWait_ATimeoutEndsTheWaitWithAnOutcome(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, fmt.Sprintf(timedWaitFlow, "50ms"))
	engine := harness.engine(t)

	outcome := <-runInBackground(engine, testItem)

	require.ErrorContains(t, outcome.err, "TIMED OUT")
}

func TestWait_AnAnswerBeforeTheTimeoutIsNotTimedOut(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, fmt.Sprintf(timedWaitFlow, "1h"))
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)
	require.NoError(t, client.Answer(testItem, "go on"))

	outcome := <-done

	require.NoError(t, outcome.err)
}

func TestWait_ATimeoutThatIsNotADurationIsRefused(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, fmt.Sprintf(timedWaitFlow, "soon"))
	engine := harness.engine(t)

	outcome := <-runInBackground(engine, testItem)

	require.ErrorContains(t, outcome.err, "not a duration")
}

func TestWait_ATimeoutOfZeroOrLessIsRefusedAndNotTakenAsNoDeadline(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{"zero seconds": "0s", "bare zero": "0", "negative": "-5s", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.flowIs(t, fmt.Sprintf(timedWaitFlow, text))
			engine := harness.engine(t)

			outcome := <-runInBackground(engine, testItem)

			require.ErrorContains(t, outcome.err, "wait: timeout is")
			require.ErrorContains(t, outcome.err, "must be above zero")
		})
	}
}

// The check reads literals only. A timeout that the file computes reaches the run, which refuses it as well.
func TestWait_AComputedTimeoutOfZeroIsRefusedByTheRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, "def main(item):\n    wait(name = \"ask\", question = \"What now?\", timeout = str(1 - 1) + \"s\")\n")
	engine := harness.engine(t)

	outcome := <-runInBackground(engine, testItem)

	require.ErrorContains(t, outcome.err, `wait: timeout is "0s", which must be above zero`)
}

func TestWait_NoTimeoutKeywordStillWaitsForTheAnswer(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, "def main(item):\n    r = wait(name = \"ask\", question = \"What now?\")\n    if r.timed_out:\n        fail(\"TIMED OUT\")\n")
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)
	require.NoError(t, client.Answer(testItem, "go on"))

	require.NoError(t, (<-done).err)
}
