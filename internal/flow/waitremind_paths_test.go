package flow

import (
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// farOff is a moment whose year no JSON text holds, so an event that carries it cannot be written.
var farOff = time.Date(10000, time.January, 2, 0, 0, 0, 0, time.UTC)

// inFlow runs body as the workflow of an item over a store of its own, and returns its error. The body gets the context of the workflow.
func inFlow(t *testing.T, body func(ctx dbos.Context) error) error {
	t.Helper()

	state, err := openStore(newHarness(t).opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = state.log.Close() })

	flowCtx, err := dbos.NewContext(t.Context(), dbos.Config{
		AppName: appName, DatabaseURL: state.url, ApplicationVersion: applicationVersion(testVersion), ExecutorID: executorName,
	})
	require.NoError(t, err)

	workflow := func(ctx dbos.Context, _ string) (string, error) { return "", body(ctx) }

	dbos.RegisterWorkflow(flowCtx, workflow, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(flowCtx))
	t.Cleanup(func() { assert.NoError(t, dbos.Shutdown(flowCtx, shutdownTimeout)) })

	handle, err := dbos.RunWorkflow(flowCtx, workflow, stuckItemName, dbos.WithWorkflowID(stuckItemID))
	require.NoError(t, err)

	if body != nil {
		sendVoiceSoon(t, flowCtx)
	}

	_, err = handle.GetResult(dbos.WithHandleTimeout(eventTimeout))

	return err
}

// sendVoiceSoon sends the voice of ana to the workflow. A workflow that never receives it leaves the message unread.
func sendVoiceSoon(t *testing.T, flowCtx dbos.Context) {
	t.Helper()

	require.NoError(t, dbos.Send(flowCtx, stuckItemID, voice{Wait: "w", From: "ana", Text: "yes"}, voiceTopic))
}

func TestWaitRemind_ADeadlineSoFarAheadThatItsReminderCannotBeShownFailsTheWait(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.migrated(t)

	deps := harness.deps()
	deps.Clock.Reading = func() time.Time { return time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC) }

	engine, err := NewEngine(t.Context(), harness.opts, deps)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	err = inFlow(t, func(ctx dbos.Context) error {
		_, withinErr := engine.within(ctx, testItem, 48*time.Hour, []float64{0.9}, &Asked{From: []string{"ana"}}, &tally{})

		return withinErr
	})

	require.ErrorContains(t, err, "cannot show who it asks")
}

func TestWaitRemind_AVoiceThatCannotBeShownFailsTheCount(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	err := inFlow(t, func(ctx dbos.Context) error {
		asked := &Asked{Wait: "w", From: []string{"ana"}, Quorum: 1, NextRemind: farOff}
		plan := schedule{dues: []time.Time{farOff}, end: time.Now().Add(time.Hour)}

		_, err := engine.collectReminding(ctx, testItem, plan, asked, &tally{answers: map[string]string{}})

		return err
	})

	require.ErrorContains(t, err, "cannot show who it asks")
}

func TestWaitRemind_AReminderWhoseNextMomentCannotBeShownFailsTheWait(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))
	now := time.Now()

	err := inFlow(t, func(ctx dbos.Context) error {
		plan := schedule{dues: []time.Time{now.Add(-time.Hour), farOff}, end: now.Add(time.Hour)}

		_, err := engine.remindDue(ctx, testItem, plan, &Asked{From: []string{"ana"}})

		return err
	})

	require.ErrorContains(t, err, "cannot show who it asks")
}

func TestWaitRemind_ACallOutsideARunRecordsNothingAndFails(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))
	now := time.Now()
	plan := schedule{dues: []time.Time{now.Add(-time.Hour)}, end: now.Add(time.Hour)}

	_, err := engine.collectReminding(engine.ctx, testItem, plan, &Asked{}, &tally{})
	require.Error(t, err, "the receive needs a run")

	_, err = engine.remindDue(engine.ctx, testItem, plan, &Asked{})
	require.Error(t, err, "the look at the clock is a recorded step of a run")

	require.Error(t, engine.remind(engine.ctx, testItem, plan, &Asked{}, 1), "the reminder is a recorded step of a run")
}

func TestWait_ARemindThatOnlyTheRunCanReadIsRefused(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ call, want string }{
		"share above one":  {`to = ["a"], timeout = "1h", remind = big`, "remind"},
		"no deadline":      {`to = ["a"], remind = ok`, "timeout"},
		"no people to ask": {`timeout = "1h", remind = ok`, "to"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.flowIs(t, "big = [2.0]\nok = [0.5]\n\ndef main(item):\n    wait(question = \"q\", "+tc.call+")\n")
			engine := harness.engine(t)

			outcome := <-runInBackground(engine, testItem)

			require.Error(t, outcome.err)
			assert.Contains(t, outcome.err.Error(), tc.want)
		})
	}
}
