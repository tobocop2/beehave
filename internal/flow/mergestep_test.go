package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/review"
)

func shipFlow(extra string) string {
	return "workflow \"w\" {}\nmerge \"ship\" {\n  head = \"" + gatedHead + "\"\n" + extra + "}\n"
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

	_, err := walkText(t, h, "workflow \"w\" {}\nmerge \"ship\" {\n  head = env.OWNER_LOGIN\n}\n")

	require.Error(t, err)
	require.ErrorContains(t, err, "OWNER_LOGIN")
	assert.NotContains(t, h.life.seen(), "ready:"+testItem)
}

func TestMergeStep_MergeTrueMergesAtThePinnedHead(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	result, err := walkText(t, h, shipFlow("  merge = true\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + " " + testItem + " " + gatedHead}, h.merge.asked)
	assert.Equal(t, "merged", result.Merge)
}

func TestMergeStep_OurOwnWorkIsLeftToTheOwnerAndTheStepWatches(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.OwnWork})

	result, err := walkText(t, h, shipFlow("  merge = true\n"))

	require.NoError(t, err)
	assert.Equal(t, []int{testPull}, h.watch.reads)
	assert.Equal(t, "merged", result.Merge)
}

func TestMergeStep_ARefusedMergeFailsTheStep(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Refused, Lines: []string{"checks are red"}})

	_, err := walkText(t, h, shipFlow("  merge = true\n"))

	require.ErrorIs(t, err, errMergeRefused)
	assert.Empty(t, h.watch.reads)
}

func TestMergeStep_MergeTrueInABuildWithoutAMergerRefusesAndNeverMarksReady(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.merge = nil

	_, err := walkText(t, h, shipFlow("  merge = true\n"))

	require.ErrorIs(t, err, errNoMerger)
	assert.NotContains(t, h.life.seen(), "ready:"+testItem)
	assert.Empty(t, h.watch.reads)
}

func TestMergeStep_APullRequestThatIsNotReadyFailsTheStepBeforeAnyMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.state.Head = "moved"

	_, err := walkText(t, h, shipFlow("  merge = true\n"))

	require.ErrorIs(t, err, errNotReady)
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

	_, err := walkText(t, h, shipFlow("")+"command \"after\" {\n  run = [\"echo\", merge.ship.state, merge.ship.reason]\n}\n")

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
	file := writeFlow(t, shipFlow("  merge = true\n"))
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

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "watch of item 12")
	assert.Empty(t, h.watch.reads)
}

func TestMergeStep_AFalseWhenSkipsTheStep(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	result, err := walkText(t, h, shipFlow("  when = false\n"))

	require.NoError(t, err)
	assert.NotContains(t, h.life.seen(), "ready:"+testItem)
	assert.Empty(t, h.watch.reads)
	assert.Empty(t, result.Merge)
}

func TestMergeStep_ABadHeadOrMergeArgumentFailsTheStep(t *testing.T) {
	t.Parallel()

	_, err := walkText(t, mergeHarness(t, merge.Result{}), "workflow \"w\" {}\nmerge \"ship\" {\n  head = 3\n}\n")
	require.ErrorContains(t, err, "must be a string")

	_, err = walkText(t, mergeHarness(t, merge.Result{}), shipFlow("  merge = \"yes\"\n"))
	require.ErrorContains(t, err, "true or false")
}

func TestMergeStep_TheSettingsOfAMergingStepReachTheMerger(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.opts.Env = map[string]string{"REPO": "o/r", "LOGIN": "me", "MAIL": "me@x"}

	_, err := walkText(t, h, shipFlow(`  merge = true
  repo = env.REPO
  base = "integration"
  hold = "hold"
  own = { logins = [env.LOGIN], emails = [env.MAIL] }
  approved = 1 == 1
  forbid_text = ["(?i)bad"]
  children = "retarget"
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

	_, err := walkText(t, h, shipFlow("  merge = true\n  own = { logins = [] }\n"))

	require.NoError(t, err)
	assert.Equal(t, []MergeSettings{{}}, h.merge.given)
}

func TestMergeStep_AnUnsetEnvNameInASettingFailsTheStepAndNamesTheVariable(t *testing.T) {
	t.Parallel()

	for setting, name := range map[string]string{
		"repo": "REPO_X", "base": "BASE_X", "hold": "HOLD_X", "children": "KIDS_X",
		"own": "OWN_X", "forbid_text": "FORBID_X", "approved": "OK_X",
	} {
		t.Run(setting, func(t *testing.T) {
			t.Parallel()

			h := mergeHarness(t, merge.Result{Stop: merge.Done})
			h.opts.Env = map[string]string{}
			value := "env." + name
			switch setting {
			case "own":
				value = "{ logins = [" + value + "], emails = [] }"
			case "forbid_text":
				value = "[" + value + "]"
			case "approved":
				value += " == \"yes\""
			}

			_, err := walkText(t, h, shipFlow("  merge = true\n  "+setting+" = "+value+"\n"))

			require.ErrorContains(t, err, name)
			assert.Empty(t, h.merge.asked)
		})
	}
}

func TestMergeStep_AnEmptyEnvValueInOwnReachesTheMergerAsAnEmptyIdentity(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.opts.Env = map[string]string{"LOGIN": ""}

	_, err := walkText(t, h, shipFlow("  merge = true\n  own = { logins = [env.LOGIN], emails = [\"a@x\"] }\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{""}, h.merge.given[0].Logins, "the guard of the merge refuses an empty identity")
}

func TestMergeStep_AnOwnThatIsNotAnObjectOfStringListsFailsTheStep(t *testing.T) {
	t.Parallel()

	for _, own := range []string{`"me"`, `{ logins = "me" }`, `{ emails = [1, [2]] }`} {
		h := mergeHarness(t, merge.Result{Stop: merge.Done})

		_, err := walkText(t, h, shipFlow("  merge = true\n  own = "+own+"\n"))

		require.Error(t, err, own)
		assert.Empty(t, h.merge.asked)
	}
}

func TestMergeStep_AComputedChildrenValueThatIsNotRefuseOrRetargetFailsTheStepAndNamesTheValue(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"retrget", "Retarget", "orphan"} {
		h := mergeHarness(t, merge.Result{Stop: merge.Done})
		h.opts.Env = map[string]string{"MODE": value}

		_, err := walkText(t, h, shipFlow("  merge = true\n  children = env.MODE\n"))

		require.ErrorContains(t, err, `"`+value+`"`)
		require.ErrorContains(t, err, "refuse")
		assert.Empty(t, h.merge.asked, value)
	}

	for value, retarget := range map[string]bool{"refuse": false, "retarget": true} {
		h := mergeHarness(t, merge.Result{Stop: merge.Done})
		h.opts.Env = map[string]string{"MODE": value}

		_, err := walkText(t, h, shipFlow("  merge = true\n  children = env.MODE\n"))

		require.NoError(t, err, value)
		assert.Equal(t, retarget, h.merge.given[0].Retarget, value)
	}
}

func TestMergeStep_ASettingThatIsNotTextFailsTheStep(t *testing.T) {
	t.Parallel()

	for _, setting := range []string{"repo", "children", "forbid_text", "approved"} {
		h := mergeHarness(t, merge.Result{Stop: merge.Done})

		_, err := walkText(t, h, shipFlow("  merge = true\n  "+setting+" = 7\n"))

		require.Error(t, err, setting)
	}
}
