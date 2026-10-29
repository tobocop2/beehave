package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/review"
)

// shipFlow is a workflow file with one merge call at the gated head. extra holds more arguments, each followed by a comma.
func shipFlow(extra string) string {
	return bodyFlow(shipCall(extra))
}

func shipCall(extra string) string {
	return "merge(head = \"" + gatedHead + "\", " + extra + "name = \"ship\")\n"
}

func TestMergeStep_AWorkflowThatDoesNotSetMergeMarksReadyAndLeavesTheMergeToTheOwner(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	result, err := walkText(t, h, shipFlow(""))

	require.NoError(t, err)
	assert.Empty(t, h.merge.asked, "the step does not merge by itself")
	assert.Contains(t, h.life.seen(), "ready:"+testItem)
	assert.Equal(t, []int{testPull}, h.watch.reads)
	assert.Equal(t, "merged", result.Merge)
	assert.Empty(t, result.SentBack)
}

func TestMergeStep_AnEnvNameThatTheEnvironmentLacksFailsTheStepAndNamesTheVariable(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.opts.Env = map[string]string{}

	_, err := walkText(t, h, shipFlow(`merge = True, repo = env("REPO_X"), `))

	require.ErrorContains(t, err, "the environment variable REPO_X is not set")
	assert.Empty(t, h.merge.given)
}

func TestMergeStep_MergeTrueMergesAtThePinnedHead(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	result, err := walkText(t, h, shipFlow("merge = True, "))

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + " " + testItem + " " + gatedHead}, h.merge.asked)
	assert.Equal(t, "merged", result.Merge)
}

func TestMergeStep_OurOwnWorkIsLeftToTheOwnerAndTheStepWatches(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.OwnWork})

	result, err := walkText(t, h, shipFlow("merge = True, "))

	require.NoError(t, err)
	assert.Equal(t, []int{testPull}, h.watch.reads)
	assert.Equal(t, "merged", result.Merge)
}

func TestMergeStep_ARefusedMergeFailsTheStep(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Refused, Lines: []string{"checks are red"}})

	_, err := walkText(t, h, shipFlow("merge = True, "))

	require.ErrorContains(t, err, errMergeRefused.Error())
	assert.Empty(t, h.watch.reads)
}

func TestMergeStep_MergeTrueInABuildWithoutAMergerRefusesAndNeverMarksReady(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.merge = nil

	_, err := walkText(t, h, shipFlow("merge = True, "))

	require.ErrorContains(t, err, errNoMerger.Error())
	assert.NotContains(t, h.life.seen(), "ready:"+testItem)
	assert.Empty(t, h.watch.reads)
}

func TestMergeStep_APullRequestThatIsNotReadyFailsTheStepBeforeAnyMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.state.Head = "moved"

	_, err := walkText(t, h, shipFlow("merge = True, "))

	require.ErrorContains(t, err, errNotReady.Error())
	assert.Empty(t, h.merge.asked)
	assert.NotContains(t, h.life.seen(), "ready:"+testItem)
}

func TestMergeStep_ASendBackRecordsTheReasonAndSetsTheDraft(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.watch.outcomes = []review.Outcome{{Kind: review.SendBack, Reason: "a change was requested"}}

	result, err := walkText(t, h, shipFlow(""))

	require.NoError(t, err)
	assert.Equal(t, "sent_back", result.Merge)
	assert.Equal(t, "a change was requested", result.SentBack)

	_, drafts := h.watch.seen()
	assert.Equal(t, []int{testPull}, drafts)
}

func TestMergeStep_TheOutputsFeedLaterSteps(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.watch.outcomes = []review.Outcome{{Kind: review.SendBack, Reason: "conflict"}}

	_, err := walkBody(t, h, "ship = "+shipCall("")+"command(run = [\"echo\", ship.state, ship.reason])\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"echo sent_back conflict"}, h.commands.ran())
}

func TestMergeStep_StatusShowsOwnerMergeWhileTheStepWaitsAndTheFinalStateAfterIt(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.watch.outcomes = []review.Outcome{{Kind: review.Waiting}, {Kind: review.Merged}}
	h.sleep = time.Second

	engine := h.engine(t)
	client := h.client(t, testVersion)
	file := writeFlow(t, shipFlow(""))
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool {
		rows, err := client.Status()

		return err == nil && len(rows) == 1 && rows[0].Merge == "owner_merge"
	})

	require.NoError(t, (<-done).err)

	rows, err := client.Status()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "merged", rows[0].Merge)
}

func TestMergeStep_AStepThatMergesNeverShowsThatItWaitsForTheOwner(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.watch.outcomes = []review.Outcome{{Kind: review.Waiting}, {Kind: review.Merged}}
	h.sleep = time.Second

	engine := h.engine(t)
	client := h.client(t, testVersion)
	file := writeFlow(t, shipFlow("merge = True, "))
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	for finished := false; !finished; {
		select {
		case outcome := <-done:
			require.NoError(t, outcome.err)

			finished = true
		case <-time.After(20 * time.Millisecond):
			rows, err := client.Status()
			require.NoError(t, err)

			for _, row := range rows {
				assert.NotEqual(t, "owner_merge", row.Merge)
			}
		}
	}

	assert.Len(t, h.merge.asked, 1)
}

func TestMergeStep_TheWatchSleepsForTheWatchTimeBetweenTwoReads(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.watch.outcomes = []review.Outcome{{Kind: review.Waiting}, {Kind: review.Waiting}, {Kind: review.Merged}}
	h.sleep = 300 * time.Millisecond

	started := time.Now()
	result, err := walkText(t, h, shipFlow(""))

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
	assert.GreaterOrEqual(t, time.Since(started), 2*h.sleep, "each of the two waiting reads is followed by one sleep")
}

func TestMergeStep_AWatchThatCannotReadThePullRequestFailsTheStep(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.failFrom = 2

	_, err := walkText(t, h, shipFlow(""))

	require.ErrorContains(t, err, errFake.Error())
	require.ErrorContains(t, err, "watch of item 12")
	assert.Empty(t, h.watch.reads)
}

func TestMergeStep_AMergeCallInAFalseBranchRunsNothing(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	result, err := walkBody(t, h, "if False:\n    "+shipCall(""))

	require.NoError(t, err)
	assert.NotContains(t, h.life.seen(), "ready:"+testItem)
	assert.Empty(t, h.watch.reads)
	assert.Empty(t, result.Merge)
}

func TestMergeStep_ABadHeadOrMergeArgumentFailsTheStep(t *testing.T) {
	t.Parallel()

	_, err := walkBody(t, mergeHarness(t, merge.Result{}), "merge(head = 3)\n")
	require.ErrorContains(t, err, "head")

	_, err = walkText(t, mergeHarness(t, merge.Result{}), shipFlow("merge = \"yes\", "))
	require.ErrorContains(t, err, "merge")
}

func TestMergeStep_TheSettingsOfAMergingStepReachTheMerger(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.opts.Env = map[string]string{"REPO": "o/r", "LOGIN": "me", "MAIL": "me@x"}

	_, err := walkText(t, h, shipFlow(`merge = True,
repo = env("REPO"),
base = "integration",
hold = "hold",
own = {"logins": [env("LOGIN")], "emails": [env("MAIL")]},
approved = 1 == 1,
forbid_text = ["(?i)bad"],
children = "retarget",
`))

	require.NoError(t, err)
	assert.Equal(t, []MergeSettings{{
		Repo: "o/r", Base: "integration", Hold: "hold", Logins: []string{"me"}, Emails: []string{"me@x"},
		ForbidText: []string{"(?i)bad"}, Approved: true, Retarget: true,
	}}, h.merge.given)
}

func TestMergeStep_ASettingLeftOutIsEmptyAndApprovedIsFalse(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, shipFlow("merge = True, own = {\"logins\": []}, "))

	require.NoError(t, err)
	assert.Equal(t, []MergeSettings{{}}, h.merge.given)
}

func TestMergeStep_AnEmptyEnvValueInOwnReachesTheMergerAsAnEmptyIdentity(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.opts.Env = map[string]string{"LOGIN": ""}

	_, err := walkText(t, h, shipFlow("merge = True, own = {\"logins\": [env(\"LOGIN\")], \"emails\": [\"a@x\"]}, "))

	require.NoError(t, err)
	assert.Equal(t, []string{""}, h.merge.given[0].Logins, "the guard of the merge refuses an empty identity")
}

func TestMergeStep_AnOwnThatIsNotADictOfStringListsFailsTheStep(t *testing.T) {
	t.Parallel()

	for _, own := range []string{`"me"`, `{"logins": "me"}`, `{"emails": [1, [2]]}`} {
		h := mergeHarness(t, merge.Result{Stop: merge.Done})

		_, err := walkText(t, h, shipFlow("merge = True, own = "+own+", "))

		require.Error(t, err, own)
		assert.Empty(t, h.merge.asked)
	}
}

func TestMergeStep_AComputedChildrenValueThatIsNotRefuseOrRetargetFailsTheStepAndNamesTheValue(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"retrget", "Retarget", "orphan"} {
		h := mergeHarness(t, merge.Result{Stop: merge.Done})
		h.opts.Env = map[string]string{"MODE": value}

		_, err := walkText(t, h, shipFlow("merge = True, children = env(\"MODE\"), "))

		require.ErrorContains(t, err, `"`+value+`"`)
		require.ErrorContains(t, err, "refuse")
		assert.Empty(t, h.merge.asked, value)
	}

	for value, retarget := range map[string]bool{"refuse": false, "retarget": true} {
		h := mergeHarness(t, merge.Result{Stop: merge.Done})
		h.opts.Env = map[string]string{"MODE": value}

		_, err := walkText(t, h, shipFlow("merge = True, children = env(\"MODE\"), "))

		require.NoError(t, err, value)
		assert.Equal(t, retarget, h.merge.given[0].Retarget, value)
	}
}

func TestMergeStep_ASettingThatIsNotTextFailsTheStep(t *testing.T) {
	t.Parallel()

	for _, setting := range []string{"repo", "children", "forbid_text", "approved", "tree"} {
		h := mergeHarness(t, merge.Result{Stop: merge.Done})

		_, err := walkText(t, h, shipFlow("merge = True, "+setting+" = 7, "))

		require.Error(t, err, setting)
	}
}
