package merge_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

// The tests below are the end-to-end scripts of the remediation example. Each one builds its request from the values of the example
// (the forbidden text) or from the shape of its merge step, and runs the guards on the forge fake.

// ownedPull is a pull request whose only commit carries the owner address and a linked account.
func ownedPull() map[string]any {
	return map[string]any{"commits": []any{commitJSON(meMail, "linked", "fix", "")}}
}

func TestScript_M13_OwnWorkWithoutApprovedIsRefusedWithNoWriteAndWithApprovedIsMerged(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, ownedPull())
	refused := w.run(request())

	assert.Equal(t, merge.OwnWork, refused.Stop)
	assert.Empty(t, w.writes, "no write call for refused own work")

	approved := request()
	approved.Approved = true
	w = newWorld(t).set(t, ownedPull())
	result := w.run(approved)

	assert.Equal(t, merge.Done, result.Stop, strings.Join(result.Lines, "\n"))
	assert.Len(t, merges(w), 1)
}

func TestScript_M13_ApprovedLiftsOnlyTheOwnWorkRule(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"commits": []any{commitJSON(meMail, "", "x", "Generated with [Claude Code]")}})
	req := request()
	req.Approved = true
	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Empty(t, merges(w))
}

func TestScript_M31_AHeldPullRequestIsNotMergedAndTheStepSaysWhy(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"labels": []any{map[string]any{"name": "hold"}}})
	req := request()
	req.Hold = "hold"
	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: #5 has the label hold, so it is held. Remove the label to release it.", last(result))
	assert.Empty(t, w.writes)
}

func TestScript_M31_TheHoldLabelMatchesWithoutRegardToCase(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"Hold", "HOLD"} {
		w := newWorld(t).set(t, map[string]any{"labels": []any{map[string]any{"name": name}}})
		req := request()
		req.Hold = "hold"
		result := w.run(req)

		assert.Equal(t, merge.Refused, result.Stop, name)
		assert.Contains(t, last(result), "has the label hold", name)
		assert.Empty(t, w.writes, name)
	}
}

func TestScript_M31_NoHoldLabelInTheRequestHoldsNothing(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"labels": []any{map[string]any{"name": "hold"}}})

	assert.Equal(t, merge.Done, w.run(request()).Stop)
}

func TestScript_M08_ABodyWithAnAttributionLineIsRefusedAndTheLineNumberIsInTheOutput(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"body": "one\ntwo\nCo-Authored-By: Claude <noreply@anthropic.com>"})
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "  3:Co-Authored-By: Claude <noreply@anthropic.com>", last(result))
	assert.Empty(t, w.writes)
}

func TestScript_M08_ACommitHeadlineAndACommitBodyAreRefusedToo(t *testing.T) {
	t.Parallel()

	for name, commit := range map[string]map[string]any{
		"headline": commitJSON("dev@x", dev, "Generated with [Claude Code]", ""),
		"body":     commitJSON("dev@x", dev, "fix", "Co-Authored-By: Claude"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t).set(t, map[string]any{"commits": []any{commit}})

			assert.Equal(t, merge.Refused, w.run(request()).Stop)
			assert.Empty(t, merges(w))
		})
	}
}

func TestScript_M08_TheEngineForbidsNothingOnItsOwn(t *testing.T) {
	t.Parallel()

	req := request()
	req.ForbidText = nil
	w := newWorld(t).set(t, map[string]any{"body": "Generated with [Claude Code]"})

	assert.Equal(t, merge.Done, w.run(req).Stop)
}

func TestScript_M08_APatternOfTheRequestIsTheRule(t *testing.T) {
	t.Parallel()

	req := request()
	req.ForbidText = []*regexp.Regexp{regexp.MustCompile("(?i)wip")}
	w := newWorld(t).set(t, map[string]any{"body": "fine\nWIP do not ship"})

	assert.Equal(t, "  2:WIP do not ship", last(w.run(req)))
}

func TestScript_R07_M12_M06_AChangeRequestAnOpenThreadAndAMissingCheckEachRefuseAndNoSettingLiftsThem(t *testing.T) {
	t.Parallel()

	approved := request()
	approved.Approved = true

	for name, arrange := range map[string]func(*world){
		"a change request": func(w *world) { w.requests = []model.ChangeRequest{{Login: "rev", Commit: sha}} },
		"an open thread":   func(w *world) { w.threads = []model.ReviewThread{{}} },
		"a missing check":  func(w *world) { w.checks = nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			arrange(w)
			result := w.run(approved)

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Empty(t, merges(w))
		})
	}
}

func TestScript_M19_C18_AHeadThatMovesBetweenTheReviewAndTheMergeCallIsRefusedBeforeTheCall(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	later := pullJSON(t, map[string]any{"headRefOid": moved})
	w.later = &later
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "head moved to "+moved)
	assert.Empty(t, merges(w))
}

func TestScript_M19_C18_TheMergeCallCarriesThePinOfTheGatedHead(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	assert.Equal(t, merge.Done, w.run(request()).Stop)
	assert.Equal(t, []string{"MergePR 5 " + sha}, merges(w))
}

func TestScript_C19_AChildWithTheHeadAsBaseBlocksTheMerge(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.children = append(w.children, pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"}))
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines, "REFUSE: #5 is the base of #7. A squash merge orphans them.")
	assert.Empty(t, w.writes)
}

func TestScript_C19_RetargetMovesEachChildOntoTheBaseBeforeTheMerge(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.children = append(w.children, pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"}), pullJSON(t, map[string]any{"number": 8, "baseRefName": "feat"}))
	req := request()
	req.Retarget = true
	result := w.run(req)

	assert.Equal(t, merge.Done, result.Stop, strings.Join(result.Lines, "\n"))
	assert.Equal(t, []string{"SetBase 7 main", "SetBase 8 main", "MergePR 5 " + sha}, w.writes)
	assert.Contains(t, result.Lines, "retargeted #7 onto main before the merge of #5")
}

func TestScript_C18_ABaseThatMovesBetweenTheTwoReadsRefusesBeforeTheMergeCall(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.tipReads = []tipRead{{sha: tipSHA, found: true}, {sha: moved, found: true}}
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: main moved from "+tipSHA+" to "+moved+" while preparing. Merge the new tip into the branch and gate it again.", last(result))
	assert.Empty(t, w.writes, "no write call, so no merge")
}

func TestScript_C18_AnUnmovedBaseIsReadAtTheStartAndRightBeforeTheMergeCall(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	result := w.run(request())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, []string{"main", "main", "main"}, w.tipsOf, "two reads before the merge call and one after it")
}

func TestScript_C18_ABaseThatCannotBeReadRefusesAtEitherRead(t *testing.T) {
	t.Parallel()

	good := tipRead{sha: tipSHA, found: true}
	for name, reads := range map[string][]tipRead{
		"start error":    {{err: errDown}},
		"start missing":  {{}},
		"before error":   {good, {err: errDown}},
		"before missing": {good, {}},
	} {
		w := newWorld(t)
		w.tipReads = reads
		result := w.run(request())

		assert.Equal(t, merge.Refused, result.Stop, name)
		assert.Contains(t, last(result), "cannot read the tip of main", name)
		assert.Empty(t, w.writes, name)
	}
}

// twoChildren is a world where the pull request has two children and the request retargets them.
func twoChildren(t *testing.T) (*world, merge.Request) {
	t.Helper()

	w := newWorld(t)
	w.children = append(w.children, pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"}), pullJSON(t, map[string]any{"number": 8, "baseRefName": "feat"}))
	req := request()
	req.Retarget = true

	return w, req
}

func TestScript_C19_WhenTheSecondMoveFailsTheFirstChildStaysMovedAndTheMergeDoesNotHappen(t *testing.T) {
	t.Parallel()

	w, req := twoChildren(t)
	w.failBase = map[int]bool{8: true}
	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, []string{"SetBase 7 main", "SetBase 8 main"}, w.writes, "no merge call")
	assert.Contains(t, result.Lines, "retargeted #7 onto main before the merge of #5")
	assert.Equal(t, "REFUSE: cannot move #8 onto main: SetBase 8: down. Moved: #7. Not moved: #8. #5 is not merged. Move #8 by hand, then run the merge again.", last(result))
}

func TestScript_C19_ARefusalOfAnyLaterGuardMovesNoChild(t *testing.T) {
	t.Parallel()

	later := pullJSON(t, map[string]any{"headRefOid": moved})
	body := map[string]any{"body": "Generated with [Claude Code]"}

	for name, set := range map[string]func(*world){
		"attribution":    func(w *world) { w.set(t, body) },
		"change request": func(w *world) { w.requests = []model.ChangeRequest{{Login: "rev", Commit: sha}} },
		"head moved":     func(w *world) { w.later = &later },
		"base moved":     func(w *world) { w.tipReads = []tipRead{{sha: tipSHA, found: true}, {sha: moved, found: true}} },
		"native stack":   func(w *world) { w.stackQueue = []string{"", "S1"} },
	} {
		w, req := twoChildren(t)
		set(w)
		result := w.run(req)

		assert.Equal(t, merge.Refused, result.Stop, name+"\n"+strings.Join(result.Lines, "\n"))
		assert.Empty(t, w.writes, name)
	}
}

func TestScript_C19_AChildThatCannotBeMovedRefusesTheMerge(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.children = append(w.children, pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"}))
	w.fails["SetBase"] = true
	req := request()
	req.Retarget = true
	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "cannot move #7 onto main")
	assert.Contains(t, last(result), "Moved: none. Not moved: #7.")
	assert.Empty(t, merges(w))
}
