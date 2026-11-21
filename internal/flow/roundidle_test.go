package flow

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// These tests run the round of the example to the question that it asks when no item can move: it names each listed item, its state and the step of the owner.

const idleItem = "432"

const idleLabel = "beehave-round"

const idleFirstSentence = "The round has no item it can move."

// idleRig is a round over the item list `items`, whose queue serves the rows on every read, so each pass sees the state of the store.
func idleRig(t *testing.T, items string, rows ...model.Item) *roundRig {
	t.Helper()

	rig := newRoundRig(t, items)
	rig.setRound("merge", false)

	// A round with no item list selects the rows by the label.
	if items == "" {
		rig.setRound("label", idleLabel)

		for i := range rows {
			rows[i].Labels = []string{idleLabel}
		}
	}

	rig.q.items = rows
	rig.h.queue = rig.q

	return rig
}

// idlePrior runs `item` once before the round through a flow file that returns, fails when fail is true, or waits for the owner when wait is true.
// A run that waits is queued on the items queue, so it takes a slot, and it stays open until the test ends.
func idlePrior(t *testing.T, engine *Engine, item string, how string) {
	t.Helper()

	body := map[string]string{
		"done": "def main(item):\n    return None\n",
		"fail": "def main(item):\n    fail(\"the agent wrote no output file\")\n",
		"wait": "def main(item):\n    wait(\"go?\", name = \"ask\")\n",
		"stop": "def main(item):\n    command(name = \"preflight\", run = [\"preflight\"], stop_on_rc = [2])\n",
	}[how]

	file := filepath.Join(t.TempDir(), "prior.star")
	require.NoError(t, os.WriteFile(file, []byte(body), 0o600))

	if how == "wait" {
		in, err := pinnedInput(item, testProject, file)
		require.NoError(t, err)

		_, err = dbos.RunWorkflow(engine.ctx, engine.item, in, dbos.WithQueue(engine.itemQueues[1]), dbos.WithWorkflowID(runID(item, 1)))
		require.NoError(t, err)

		return
	}

	_, err := engine.RunFile(item, testProject, file)
	if how == "fail" {
		require.Error(t, err, "the prior run fails")
	} else {
		require.NoError(t, err, "the prior run ends done")
	}
}

// idleRun runs the round on the engine and answers each question of the round (and no question of another run). before runs with the client just before the nth answer.
func idleRun(t *testing.T, rig *roundRig, engine *Engine, before func(n int, client *Client)) {
	t.Helper()

	rig.last = engine
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, rig.file)
		done <- runOutcome{result: result, err: err}
	}()

	client := rig.h.client(t, testVersion)
	owner := &answering{}

	out := waitOn(t, waiting[runOutcome]{what: "the round to end", ch: done, read: func() (runOutcome, bool, error) {
		idleAnswerOnce(t, rig, client, owner, before)

		return runOutcome{}, false, nil
	}})
	require.NoError(t, out.err)
}

// idleAnswerOnce answers the question of the round, once, after the step before of the owner.
func idleAnswerOnce(t *testing.T, rig *roundRig, client *Client, owner *answering, before func(n int, client *Client)) {
	t.Helper()

	infos, err := client.Status()
	if err != nil {
		return
	}

	at := slices.IndexFunc(infos, func(info RunInfo) bool { return info.Item == testItem && info.Question != "" })
	if at < 0 {
		owner.open = false

		return
	}

	if owner.open || !recordsItsReceive(client, infos[at].ID) {
		return
	}

	rig.questions = append(rig.questions, infos[at].Question)

	if before != nil {
		before(owner.given, client)
	}

	require.NoError(t, client.AnswerAs(testItem, shipOwner, rig.answerFor(owner.given)))

	owner.given++
	owner.open = true
}

// idleExists reports whether the store holds the run id.
func idleExists(t *testing.T, client *Client, id string) bool {
	t.Helper()

	rows, err := dbos.ListWorkflows(client.flow, dbos.WithFilterWorkflowIDs(id))
	require.NoError(t, err)

	return len(rows) > 0
}

func TestRoundIdle_AFailedListedItemIsNamedWithItsStateAndBothOwnerSteps(t *testing.T) {
	t.Parallel()

	rig := idleRig(t, idleItem, queueItem(idleItem, model.SeverityP1))
	engine := rig.engine(t)
	idlePrior(t, engine, idleItem, "fail")

	idleRun(t, rig, engine, nil)

	require.Len(t, rig.questions, 1)
	question := rig.questions[0]
	assert.True(t, strings.HasPrefix(question, idleFirstSentence), "the first sentence stays for the golden: %s", question)
	assert.Contains(t, question, "item 432 is failed: run beehave resume 432")
	assert.Contains(t, question, "run beehave cancel 432, and the next pass starts a new attempt")
	assert.Contains(t, question, "Then answer CONTINUE to check the queue again, or STOP to end the round.")
	assert.NotContains(t, question, "integration pull request", "the text does not send the owner to the pull request")
}

func TestRoundIdle_AContinueWithNoOwnerStepStartsNoNewAttemptAndAsksAgainWithTheItem(t *testing.T) {
	t.Parallel()

	rig := idleRig(t, idleItem, queueItem(idleItem, model.SeverityP1))
	rig.answers = []string{"CONTINUE", "STOP"}
	engine := rig.engine(t)
	idlePrior(t, engine, idleItem, "fail")

	idleRun(t, rig, engine, nil)

	assert.False(t, idleExists(t, rig.h.client(t, testVersion), runID(idleItem, 2)), "the round starts no attempt after a failed one on its own")
	require.Len(t, rig.questions, 2, "CONTINUE asks again")
	assert.Contains(t, rig.questions[1], "item 432 is failed")
}

func TestRoundIdle_ACancelOfTheFailedRunThenContinueStartsTheNextAttempt(t *testing.T) {
	t.Parallel()

	rig := idleRig(t, idleItem, queueItem(idleItem, model.SeverityP1))
	rig.answers = []string{"CONTINUE", "STOP"}
	engine := rig.engine(t)
	idlePrior(t, engine, idleItem, "fail")

	idleRun(t, rig, engine, func(n int, client *Client) {
		if n == 0 {
			require.NoError(t, client.Cancel(idleItem))
		}
	})

	assert.True(t, idleExists(t, rig.h.client(t, testVersion), runID(idleItem, 2)), "a canceled last run lets the round start the next attempt")
	assert.Contains(t, startedChildren(rig), idleItem)
}

func TestRoundIdle_AHeldListedItemIsNamedAsOnHold(t *testing.T) {
	t.Parallel()

	held := queueItem(idleItem, model.SeverityP1)
	held.Hold = true
	rig := idleRig(t, idleItem, held)

	_, err := rig.run(t)
	require.NoError(t, err)

	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "item 432 is on hold: take the hold off the item or its pull request")
}

func TestRoundIdle_ADoneListedItemIsNamedAsDone(t *testing.T) {
	t.Parallel()

	rig := idleRig(t, idleItem, queueItem(idleItem, model.SeverityP1))
	engine := rig.engine(t)
	idlePrior(t, engine, idleItem, "done")

	idleRun(t, rig, engine, nil)

	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "item 432 is done.")
}

func TestRoundIdle_AListedItemThatTheQueueHasNoRowForIsNamedAndAnUnlistedRowStaysOut(t *testing.T) {
	t.Parallel()

	rig := idleRig(t, idleItem, queueItem("21", model.SeverityP1))

	_, err := rig.run(t)
	require.NoError(t, err)

	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "item 432 is not in the queue: it is closed, or it does not carry vars.round.label")
	assert.Empty(t, startedChildren(rig), "an unlisted ready row stays out")
}

func TestRoundIdle_AListedItemWithAnOpenRunOutsideTheRoundIsNamedAndAReadyOneWithNoFreeSlotToo(t *testing.T) {
	t.Parallel()

	rig := idleRig(t, "7 "+idleItem, queueItem("7", model.SeverityP1), queueItem(idleItem, model.SeverityP1))
	rig.h.opts.MaxConcurrent = 1
	engine := rig.engine(t)
	idlePrior(t, engine, "7", "wait")
	client := rig.h.client(t, testVersion)
	waitOn(t, condition(func() bool { return idleExists(t, client, runID("7", 1)) }))

	idleRun(t, rig, engine, nil)

	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "item 7 has an open run outside this round: beehave status shows it")
	assert.Contains(t, rig.questions[0], "item 432 is ready, and the items queue has no free slot")
	assert.Empty(t, startedChildren(rig), "the one slot is taken, so the ready item waits")
}

// stopAtPreflight makes the command preflight of the rig end with the stop code and print the reason.
func stopAtPreflight(rig *roundRig) {
	other := rig.h.commands.reply

	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "preflight" {
			return CommandOutput{Stdout: "the pull request is a draft that this run did not open", RC: 2}, nil
		}

		return other(request)
	}
}

func TestRoundIdle_AListedItemWhoseRunEndedWithAStopNamesTheStepAndTheReasonAndIsNotDone(t *testing.T) {
	t.Parallel()

	rig := idleRig(t, idleItem, queueItem(idleItem, model.SeverityP1))
	stopAtPreflight(rig)
	engine := rig.engine(t)
	idlePrior(t, engine, idleItem, "stop")

	idleRun(t, rig, engine, nil)

	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "item 432 stopped at command:preflight#1: the pull request is a draft that this run did not open. Fix the cause, run beehave cancel 432, and the next pass starts a new attempt.")
	assert.NotContains(t, rig.questions[0], "is done", "a stopped item is not offered as done")
}

func TestRoundIdle_ARoundWithNoItemListNamesTheStopOfEachStoppedRow(t *testing.T) {
	t.Parallel()

	rig := idleRig(t, "", queueItem(idleItem, model.SeverityP1))
	stopAtPreflight(rig)
	engine := rig.engine(t)
	idlePrior(t, engine, idleItem, "stop")

	idleRun(t, rig, engine, nil)

	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "Rows: 0 ready, 0 failed, 0 on hold, 0 done, 0 running.")
	assert.Contains(t, rig.questions[0], "Stopped: 432 at command:preflight#1: the pull request is a draft that this run did not open. Fix each cause, then run beehave cancel for each.")
}

func TestRoundIdle_ARoundWithNoItemListOverAnEmptyQueueSaysNoRowIsReady(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")

	_, err := rig.run(t)
	require.NoError(t, err)

	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "The queue has no ready row. Rows: 0 ready, 0 failed, 0 on hold, 0 done, 0 running.")
	assert.Contains(t, rig.questions[0], "Then answer CONTINUE")
}

func TestRoundIdle_ARoundWithNoItemListCountsTheRowsByStateAndNamesTheFailedIds(t *testing.T) {
	t.Parallel()

	held := queueItem("10", model.SeverityP1)
	held.Hold = true
	rig := idleRig(t, "", queueItem(idleItem, model.SeverityP1), queueItem("433", model.SeverityP1), held)
	engine := rig.engine(t)
	idlePrior(t, engine, idleItem, "fail")
	idlePrior(t, engine, "433", "fail")

	idleRun(t, rig, engine, nil)

	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "The queue has no ready row. Rows: 0 ready, 2 failed, 1 on hold, 0 done, 0 running.")
	assert.Contains(t, rig.questions[0], "Failed: 432, 433. Run beehave resume for each, or beehave cancel when the resume refuses the run.")
}

func TestRoundIdle_ARoundWithNoItemListAndAReadyRowWithNoFreeSlotSaysSo(t *testing.T) {
	t.Parallel()

	rig := idleRig(t, "", queueItem("7", model.SeverityP1), queueItem(idleItem, model.SeverityP1))
	rig.h.opts.MaxConcurrent = 1
	engine := rig.engine(t)
	idlePrior(t, engine, "7", "wait")
	client := rig.h.client(t, testVersion)
	waitOn(t, condition(func() bool { return idleExists(t, client, runID("7", 1)) }))

	idleRun(t, rig, engine, nil)

	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "The items queue has no free slot. Rows: 1 ready, 0 failed, 0 on hold, 0 done, 1 running.")
}

func TestFailedError_NamesTheResumeAndTheCancel(t *testing.T) {
	t.Parallel()

	failed := run{Status: dbos.WorkflowStatus{ID: "item-12-r1", Status: dbos.WorkflowStatusError, Error: errFake}}

	err := failedError(failed, "12")

	require.ErrorIs(t, err, errRunFailed)
	assert.ErrorContains(t, err, "Run beehave resume 12 to continue it from the last finished step. To start a new attempt instead, run beehave cancel 12")
}

func TestEngine_RunRefusesANewRunAfterAFailedOneAndNamesTheCancel(t *testing.T) {
	t.Parallel()

	_, engine, _ := failedOnOtherBuild(t)

	_, runErr := engine.Run(testItem, testProject)
	require.ErrorIs(t, runErr, errRunFailed)
	assert.ErrorContains(t, runErr, "beehave cancel "+testItem)
}

func TestClient_ResumeOnThisBuildRefusalOfAFailedRunNamesTheCancelAtEachSiteThatRefuses(t *testing.T) {
	t.Parallel()

	sentence := "End the failed run first: beehave cancel " + testItem + ". Start the item again: beehave run " + testItem
	tests := map[string]func(t *testing.T) (*harness, *Client){
		"a_step_list_that_is_not_valid": func(t *testing.T) (*harness, *Client) {
			t.Helper()

			harness, _, other := strandedHarness(t)
			setVersion(t, harness, testVersion+"+gate1.gate2.gate3")

			return harness, other
		},
		"another_step_list": func(t *testing.T) (*harness, *Client) {
			t.Helper()

			harness, _, other := strandedHarness(t)
			setVersion(t, harness, testVersion+"+other")

			return harness, other
		},
		"a_module_that_changed": func(t *testing.T) (*harness, *Client) {
			t.Helper()

			harness, _, other := strandedHarness(t)

			return harness, buildWithModule(other, moduleAgent, agentVersion+1)
		},
		"a_step_that_this_build_does_not_record": func(t *testing.T) (*harness, *Client) {
			t.Helper()

			harness, other := olderRoundRun(t, testItem, moduleLand, 4)
			recordSteps(t, harness, testItem, "merge:m#1")

			return harness, other
		},
	}

	for name, start := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness, other := start(t)
			_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET status = ? WHERE workflow_uuid = ?", string(dbos.WorkflowStatusError), testRun)
			require.NoError(t, err)
			expireLease(harness)

			resumeErr := other.ResumeOnThisBuild(testItem)

			require.ErrorIs(t, resumeErr, errStranded)
			assert.ErrorContains(t, resumeErr, sentence)
		})
	}
}

// setVersion stores the version on the run of the harness.
func setVersion(t *testing.T, h *harness, version string) {
	t.Helper()

	_, err := sqlOf(t, h).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", version, testRun)
	require.NoError(t, err)
}

func TestEndFailedFirst_NamesTheCancelOnlyForAFailedRun(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		status dbos.WorkflowStatusType
		want   string
	}{
		"a_failed_run_is_ended_first":    {dbos.WorkflowStatusError, "End the failed run first: beehave cancel 12. "},
		"a_canceled_run_needs_no_cancel": {dbos.WorkflowStatusCancelled, ""},
		"an_open_run_needs_no_cancel":    {dbos.WorkflowStatusPending, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, endFailedFirst(run{Status: dbos.WorkflowStatus{Status: test.status}}, "12"))
		})
	}
}
