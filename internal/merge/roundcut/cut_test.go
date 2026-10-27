package roundcut_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge/roundcut"
)

func request(dry bool) roundcut.Request {
	return roundcut.Request{
		Cut: roundcut.Cut{Roman: "VI", OldPR: oldPR, OldBase: oldRef, NewBase: newRef, Trunk: "main"},
		Dry: dry,
	}
}

func (w *world) cut(dry bool) roundcut.Result {
	return roundcut.Run(context.Background(), w, request(dry))
}

func last(result roundcut.Result) string {
	return result.Lines[len(result.Lines)-1]
}

func hasPrefix(result roundcut.Result, prefix string) bool {
	for _, line := range result.Lines {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}

	return false
}

func TestRun_TheNewBaseIsCutFromTheTrunkTip(t *testing.T) {
	t.Parallel()

	w := newWorld()
	result := w.cut(true)

	assert.Equal(t, roundcut.Done, result.Stop)
	assert.Contains(t, result.Lines, "main tip: "+tip)
	assert.Contains(t, result.Lines, "WOULD: create branch "+newRef+" at "+tip)
}

func TestRun_ADryRunChangesNothingAndSaysWhatItWouldDo(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.noWrites = true
	result := w.cut(true)

	assert.Equal(t, roundcut.Done, result.Stop)
	assert.Empty(t, w.writes)
	assert.Contains(t, result.Lines, "WOULD: set the base of #11 to "+newRef)
	assert.Contains(t, result.Lines, "WOULD: set the base of #12 to "+newRef)
	assert.False(t, hasPrefix(result, "SAFE"))
	assert.Len(t, w.lists, 1)
}

func TestRun_EveryOpenPullRequestOnTheOldBaseMovesToTheNewBase(t *testing.T) {
	t.Parallel()

	w := newWorld()
	result := w.cut(false)

	assert.Equal(t, roundcut.Done, result.Stop)
	assert.Equal(t, []string{
		"CreateBranch " + newRef + " " + tip,
		"SetBase 11 " + newRef,
		"SetBase 12 " + newRef,
	}, w.writes)
	assert.Equal(t, "SAFE to delete "+oldRef+". Delete it by hand after confirming nothing is stacked on it.", result.Lines[len(result.Lines)-9])
	assert.Equal(t, []string{"#11 one", "#12 two"}, result.Lines[3:5])
	assert.Contains(t, result.Lines, "open PRs to retarget: 2")
}

func TestRun_TheOldListReadsTheOldBaseAndTheEditNamesTheNewOne(t *testing.T) {
	t.Parallel()

	w := newWorld()
	req := request(false)
	req.Cut.NewBase = "staging/other"
	roundcut.Run(context.Background(), w, req)

	assert.Equal(t, []string{"1000", "--state", "open", "--base", oldRef, "--json", "number,title"}, w.lists[0])
	assert.Contains(t, w.writes, "SetBase 11 staging/other")
}

func TestRun_APullRequestStillOnTheOldBaseRefusesTheDelete(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.stuck[12] = true
	result := w.cut(false)

	assert.Equal(t, roundcut.Refused, result.Stop)
	assert.Equal(t, "REFUSE to delete "+oldRef+": 1 PRs still target it.", last(result))
	assert.Contains(t, result.Lines, "still on "+oldRef+" after retarget: 1")
	assert.False(t, hasPrefix(result, "SAFE"))
}

func TestRun_AnIntegrationPullRequestThatIsNotMergedRefusesBeforeAnythingChanges(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.state = "OPEN"
	w.mergedAt = nil
	result := w.cut(false)

	assert.Equal(t, roundcut.Refused, result.Stop)
	assert.Equal(t, []string{
		"integration PR #5: OPEN - main",
		"REFUSE: #5 is not merged. Cut the round only after it lands.",
	}, result.Lines)
	assert.Empty(t, w.writes)
}

func TestRun_APullRequestViewThatFailsIsUnreadable(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.fails["PRView"] = true
	result := w.cut(false)

	assert.Equal(t, roundcut.Refused, result.Stop)
	assert.Equal(t, []string{"REFUSE: cannot read integration pull request #5"}, result.Lines)
}

func TestRun_TheTrunkTipThatCannotBeReadStops(t *testing.T) {
	t.Parallel()

	for name, set := range map[string]func(*world){
		"a failed read": func(w *world) { w.fails["trunk"] = true },
		"a missing tip": func(w *world) { w.trunkOK = false },
	} {
		w := newWorld()
		set(w)
		result := w.cut(false)

		assert.Equal(t, roundcut.Refused, result.Stop, name)
		assert.Equal(t, "REFUSE: cannot read the tip of main", last(result), name)
		assert.Empty(t, w.writes, name)
	}
}

func TestRun_ASecondListThatFailsStopsWithTheReasonAndNoSafeLine(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.failList = 2
	result := w.cut(false)

	assert.Equal(t, roundcut.Refused, result.Stop)
	assert.Equal(t, fmt.Sprintf("cannot list pull requests (--state open --base %s --json number): down", oldRef), last(result))
	assert.False(t, hasPrefix(result, "SAFE"))
}

func TestRun_AFirstListThatFailsStopsBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.failList = 1
	result := w.cut(false)

	assert.Equal(t, roundcut.Refused, result.Stop)
	assert.Empty(t, w.writes)
}

func TestRun_AfterAFailedRetargetTheRerunFinishesTheJob(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.failWrite = 3
	first := w.cut(false)

	assert.Equal(t, roundcut.Refused, first.Stop)
	assert.Equal(t, "REFUSE: cannot set the base of #12 to "+newRef+": SetBase 12 "+newRef+": down", last(first))
	assert.Equal(t, tip, w.branches[newRef])

	before := len(w.writes)
	rerun := w.cut(false)

	assert.Equal(t, roundcut.Done, rerun.Stop)
	assert.Equal(t, []string{"SetBase 12 " + newRef}, w.writes[before:])
	assert.Contains(t, rerun.Lines, newRef+" already exists at the main tip: resuming the retarget")
	assert.True(t, hasPrefix(rerun, "SAFE to delete"))
}

func TestRun_ANewBaseAtSomeOtherCommitStillRefuses(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.branches[newRef] = other
	result := w.cut(false)

	assert.Equal(t, roundcut.Refused, result.Stop)
	assert.Contains(t, result.Lines, "REFUSE: "+newRef+" already exists at "+other+", not at the main tip "+tip+".")
	assert.Empty(t, w.writes)
}

func TestRun_ARerunAfterTheTrunkMovedRefusesRetargetsNothingAndNamesTheFix(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.branches[newRef] = tip
	w.trunk = moved
	result := w.cut(false)

	assert.Equal(t, roundcut.Refused, result.Stop)
	assert.Equal(t, []string{
		"REFUSE: " + newRef + " already exists at " + tip + ", not at the main tip " + moved + ".",
		"        If main moved since the first run and nobody pushed to " + newRef + ", move it to " + moved + " in the forge and run again.",
	}, result.Lines[len(result.Lines)-2:])
	assert.Empty(t, w.writes)
}

func TestRun_ANewBaseThatCannotBeReadOrCreatedRefuses(t *testing.T) {
	t.Parallel()

	read := newWorld()
	read.fails["branch"] = true
	result := read.cut(false)

	assert.Equal(t, roundcut.Refused, result.Stop)
	assert.Equal(t, "REFUSE: cannot read "+newRef, last(result))
	assert.Empty(t, read.writes)

	create := newWorld()
	create.failWrite = 1
	result = create.cut(false)

	assert.Equal(t, roundcut.Refused, result.Stop)
	assert.Equal(t, "REFUSE: cannot create branch "+newRef+": CreateBranch "+newRef+" "+tip+": down", last(result))
}

func TestRun_TheRemainingStepsCloseTheReport(t *testing.T) {
	t.Parallel()

	result := newWorld().cut(false)
	notes := result.Lines[len(result.Lines)-8:]

	assert.Empty(t, notes[0])
	assert.Equal(t, "Remaining steps, deliberately not automated:", notes[1])
	assert.Equal(t, "  1. Open the round VI integration pull request ("+newRef+" -> main) AFTER the first fix merges;", notes[2])
	assert.True(t, strings.HasPrefix(notes[7], "  4. Tell every live lane the new base SHA"))
}

func TestRun_TheCountOfRowsCountsATitleOnceWhateverItHolds(t *testing.T) {
	t.Parallel()

	for _, title := range []string{"a\nb", "tab\tin it"} {
		w := newWorld()
		w.pulls = []pull{{11, title, oldRef}}
		result := w.cut(true)

		assert.Contains(t, result.Lines, "open PRs to retarget: 1", title)
		assert.Equal(t, "#11 "+strings.NewReplacer("\n", " ", "\t", " ").Replace(title), result.Lines[3])
	}
}

func TestRun_ARowWithNoNumberIsListedButNotEdited(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.pulls = []pull{{0, "x", oldRef}}
	result := w.cut(true)

	require.Equal(t, roundcut.Done, result.Stop)
	assert.Contains(t, result.Lines, "#0 x")
	assert.False(t, hasPrefix(result, "WOULD: set the base"))
	assert.Empty(t, w.writes)
}
