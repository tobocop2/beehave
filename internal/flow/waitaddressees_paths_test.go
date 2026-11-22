package flow

import (
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitVoices_ReportAnEventThatCannotBeShown(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	_, err := waiter(engine).waitVoices(waitPlan{question: "q", asked: Asked{Choices: []string{"a"}}})
	require.ErrorContains(t, err, "item 12 cannot show who it asks")

	_, err = waiter(engine).listen(waitPlan{question: "q", asked: Asked{Choices: []string{"a"}}})
	require.Error(t, err, "the id of a wait is a recorded step of a run")

	_, err = waiter(engine).within(time.Hour, nil, &Asked{}, &tally{})
	require.Error(t, err, "the end of a deadline is recorded as a step of a run")
}

func TestWait_ArgumentsThatOnlyTheRunCanReadAreRefused(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ call, want string }{
		"to empty":         {`to = none`, "to is an empty list"},
		"choices empty":    {`to = ["a"], choices = none`, "choices is an empty list"},
		"quorum zero":      {`to = ["a"], quorum = zero`, "quorum must be a whole number of 1 or more"},
		"quorum no people": {`quorum = two`, "quorum needs `to`"},
		"quorum too big":   {`to = ["a"], quorum = two`, "quorum is 2, and `to` names 1 people"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.flowIs(t, "none = []\nzero = 0\ntwo = 2\n\ndef main(item):\n    wait(question = \"q\", "+tc.call+")\n")
			engine := harness.engine(t)

			outcome := receive(t, runInBackground(engine, testItem))

			require.ErrorContains(t, outcome.err, tc.want)
		})
	}
}

// stuckAsk sets a question and an ask event that is not a record, then waits for the owner.
func stuckAsk(ctx dbos.Context, _ string) (string, error) {
	if err := dbos.SetEvent(ctx, questionKey, "which?"); err != nil {
		return "", err
	}

	if err := dbos.SetEvent(ctx, askKey, 42); err != nil {
		return "", err
	}

	return awaitInbox(ctx)
}

func TestClient_StatusAndAnswerReportAnAskThatIsNotARecord(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	state, err := openStore(harness.opts)
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.log.Close() })

	flowCtx, err := dbos.NewContext(t.Context(), dbos.Config{
		AppName: appName, DatabaseURL: state.url, ApplicationVersion: applicationVersion(testVersion), ExecutorID: executorName,
	})
	require.NoError(t, err)

	dbos.RegisterWorkflow(flowCtx, stuckAsk, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(flowCtx))

	_, err = dbos.RunWorkflow(flowCtx, stuckAsk, stuckItemName, dbos.WithWorkflowID(stuckItemID))
	require.NoError(t, err)

	client := harness.client(t, testVersion)

	waitOn(t, condition(func() bool {
		_, statusErr := client.Status()

		return statusErr != nil
	}))

	require.ErrorContains(t, client.Reply(stuckItemName, Reply{Text: "go on"}), "read who "+stuckItemID+" asks")
	require.NoError(t, dbos.CancelWorkflow(flowCtx, stuckItemID))
	require.NoError(t, dbos.Shutdown(flowCtx, shutdownTimeout))
}

func TestWait_AVoiceTheWaitRefusesIsDroppedAndAWindowWithNoVoiceReceivesAgain(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, "def main(item):\n    r = wait(name = \"ask\", question = \"Go?\", to = [\"ana\"])\n    if r.answers != {\"ana\": \"yes\"}:\n        fail(\"WRONG \" + str(r.answers))\n")
	engine := launchedWith(t, harness, func(deps *Deps) { deps.Clock.OwnerWait = 20 * time.Millisecond })
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))
	waitOn(t, waiting[struct{}]{what: "the run to show who it asks", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, askedOf(t, client) != nil, nil }})

	// Several windows of 20 ms end with no voice.
	before, err := runOfEngine(t, engine).windowsEnded()
	require.NoError(t, err)
	waitOn(t, windowsEndedBy(runOfEngine(t, engine), before+5))

	require.NoError(t, sendAnswerEnvelope(client.flow, testRun, voice{From: "zed", Text: "yes"}))

	// The windows that end after the voice show that the run dropped it.
	before, err = runOfEngine(t, engine).windowsEnded()
	require.NoError(t, err)
	waitOn(t, windowsEndedBy(runOfEngine(t, engine), before+3))
	assert.Empty(t, askedOf(t, client).Heard, "a voice from someone not named counts for nothing")

	require.NoError(t, client.Reply(testItem, Reply{From: "ana", Text: "yes"}))
	require.NoError(t, receive(t, done).err)
}
