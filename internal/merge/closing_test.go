package merge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

// audited is a request that names the given issues as the issues that the pull request must close.
func audited(names ...int) merge.Request {
	req := request()
	req.Names = names

	return req
}

// listing is a forge that lists the given issues for a pull request into the default branch.
func listing(w *world, issues ...int) *world {
	w.closingRefs = &model.ClosingRefs{BaseBranch: "main", DefaultBranch: "main", Issues: issues}

	return w
}

func TestClosing_TheIssuesThatTheForgeListsEqualTheIssuesTheItemNamesAndTheMergeGoesOn(t *testing.T) {
	t.Parallel()

	w := listing(newWorld(t), 12, 13)
	result := w.run(audited(13, 12, 12))

	assert.Equal(t, merge.Done, result.Stop)
	assert.Contains(t, w.calls, "ClosingRefs")
}

func TestClosing_AnExtraIssueRefusesAndNamesBothLists(t *testing.T) {
	t.Parallel()

	w := listing(newWorld(t), 12, 99)
	result := w.run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Plain, result.Reason)
	assert.Equal(t, "REFUSE: #5 closes #12 #99 and the closes list of the member holds #12. The owner decides: fix the closing keywords or the closes list.", last(result))
	assert.Empty(t, merges(w))
}

func TestClosing_AMissingIssueRefusesAndNamesBothLists(t *testing.T) {
	t.Parallel()

	w := listing(newWorld(t))
	result := w.run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: #5 closes no issue and the closes list of the member holds #12. The owner decides: fix the closing keywords or the closes list.", last(result))
	assert.Empty(t, merges(w))
}

// pinned is a request that names the given issues as the issues that the pull request must close, and pins the set that a check read.
func pinned(names []int, set ...model.IssueRef) merge.Request {
	req := audited(names...)
	req.Pinned = append([]model.IssueRef{}, set...)

	return req
}

func TestClosing_AMemberThatPinsTheSetThatTheForgeListsNowMergesWhateverTheOrderOfThePin(t *testing.T) {
	t.Parallel()

	w := listing(newWorld(t), 12, 13)
	w.closingRefs.Elsewhere = []model.IssueRef{"other-org/other-repo#5"}
	result := w.run(pinned([]int{12, 13}, "other-org/other-repo#5", "#13", "#12", "#12"))

	assert.Equal(t, merge.Done, result.Stop)
}

func TestClosing_AMemberThatPinsASetRefusesWhenAnIssueOfThisRepositoryWasAddedAfterTheCheck(t *testing.T) {
	t.Parallel()

	w := listing(newWorld(t), 12, 13)
	result := w.run(pinned([]int{12, 13}, "#12"))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: #5 closes #12 #13 now, and closed #12 when the scope check read it. The owner decides: run the scope check again.", last(result))
	assert.Empty(t, merges(w))
}

func TestClosing_AMemberThatPinsASetRefusesWhenAnIssueOfAnotherRepositoryWasAddedAfterTheCheck(t *testing.T) {
	t.Parallel()

	w := listing(newWorld(t), 12)
	w.closingRefs.Elsewhere = []model.IssueRef{"other-org/other-repo#5"}
	result := w.run(pinned([]int{12}, "#12"))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: #5 closes #12 other-org/other-repo#5 now, and closed #12 when the scope check read it. The owner decides: run the scope check again.", last(result))
}

func TestClosing_AMemberThatPinsNoIssueRefusesWhenThePullRequestClosesOneNow(t *testing.T) {
	t.Parallel()

	w := listing(newWorld(t), 12)
	result := w.run(pinned([]int{12}))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: #5 closes #12 now, and closed no issue when the scope check read it. The owner decides: run the scope check again.", last(result))
}

func TestClosing_AMemberThatPinsNoSetIsNotHeldToOne(t *testing.T) {
	t.Parallel()

	w := listing(newWorld(t), 12)
	req := audited(12)

	assert.Nil(t, req.Pinned)
	assert.Equal(t, merge.Done, w.run(req).Stop, "a member with no check pins nothing, and the issues it names are the audit")
}

func TestClosing_AForgeThatCannotReadClosingReferencesRefusesAndNeverPasses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.closingRefs = nil
	result := w.run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Transient, result.Reason)
	assert.Contains(t, last(result), "cannot read the issues that #5 closes")
	assert.Empty(t, merges(w))
}

func TestClosing_ABranchNameThatTheForgeLeavesEmptyRefuses(t *testing.T) {
	t.Parallel()

	for name, refs := range map[string]model.ClosingRefs{
		"base":    {DefaultBranch: "main", Issues: []int{12}},
		"default": {BaseBranch: "main", Issues: []int{12}},
	} {
		w := newWorld(t)
		w.closingRefs = &refs
		result := w.run(audited(12))

		assert.Equal(t, merge.Refused, result.Stop, name)
		assert.Contains(t, last(result), "cannot read the base branch and the default branch of #5", name)
		assert.Empty(t, merges(w), name)
	}
}

func TestClosing_ABaseThatIsNotTheDefaultBranchIsAuditedAgainstTheIssuesTheForgeGives(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.closingRefs = &model.ClosingRefs{BaseBranch: "integration/r7", DefaultBranch: "main", Issues: []int{12}}

	assert.Equal(t, merge.Done, w.run(audited(12)).Stop)

	w = newWorld(t)
	w.closingRefs = &model.ClosingRefs{BaseBranch: "integration/r7", DefaultBranch: "main", Issues: []int{12, 99}}
	result := w.run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "#5 closes #12 #99 and the closes list of the member holds #12")
	assert.Empty(t, merges(w))
}

func TestClosing_ARequestThatNamesNoIssueIsAuditedAgainstNone(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	result := w.run(request())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Contains(t, w.calls, "ClosingRefs")

	w = listing(newWorld(t), 12)
	result = w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: #5 closes #12 and the closes list of the member holds no issue. The owner decides: fix the closing keywords or the closes list.", last(result), "an empty closes list is named as the closes list")
	assert.Empty(t, merges(w))
}

// worded is a forge that lists issue 12 for a pull request into the default branch, with one commit of the given message.
func worded(t *testing.T, headline, body string) *world {
	t.Helper()

	w := listing(newWorld(t), 12)

	return w.set(t, map[string]any{"commits": []any{commitJSON(dev+"@x", dev, headline, body)}})
}

func TestCommitKeywords_AKeywordForAnotherIssueRefusesAndNamesTheCommitAndTheIssue(t *testing.T) {
	t.Parallel()

	w := worded(t, "Tidy", "Fixes #99")
	result := w.run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Plain, result.Reason)
	assert.Contains(t, result.Lines, "  commit bbbbbbbbb closes #99")
	assert.Contains(t, result.Lines, "REFUSE: commit messages of #5 hold a closing keyword for an issue that the item does not name:")
	assert.Empty(t, merges(w))
}

func TestCommitKeywords_EachOfTheNineKeywordsInAnyCaseCounts(t *testing.T) {
	t.Parallel()

	for _, word := range []string{"close", "closes", "closed", "fix", "fixes", "fixed", "resolve", "resolves", "resolved", "CLOSES", "Resolved:"} {
		w := worded(t, word+" #99", "")
		result := w.run(audited(12))

		assert.Equal(t, merge.Refused, result.Stop, word)
		assert.Contains(t, result.Lines, "  commit bbbbbbbbb closes #99", word)
	}
}

func TestCommitKeywords_AKeywordInTheHeadlineCounts(t *testing.T) {
	t.Parallel()

	result := worded(t, "Closes #99 on the way", "").run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
}

func TestCommitKeywords_AKeywordForAnotherRepositoryRefuses(t *testing.T) {
	t.Parallel()

	result := worded(t, "Tidy", "Fixes other/place#7").run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines, "  commit bbbbbbbbb closes other/place#7")
}

func TestCommitKeywords_AKeywordForTheIssueOfTheItemOrOfThisRepositoryPasses(t *testing.T) {
	t.Parallel()

	result := worded(t, "Fixes #12", "Resolves owner/name#12").run(audited(12))

	assert.Equal(t, merge.Done, result.Stop)
}

func TestCommitKeywords_AKeywordForAnotherIssueOfThisRepositoryByFullNameRefuses(t *testing.T) {
	t.Parallel()

	result := worded(t, "Tidy", "Fixes Owner/Name#99").run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines, "  commit bbbbbbbbb closes #99")
}

func TestCommitKeywords_AKeywordWithAnIssueURLCountsLikeTheShortForm(t *testing.T) {
	t.Parallel()

	refused := worded(t, "Tidy", "Fixes https://github.com/owner/name/issues/99 and closes http://github.com/other/place/issues/7").run(audited(12))

	assert.Equal(t, merge.Refused, refused.Stop)
	assert.Contains(t, refused.Lines, "  commit bbbbbbbbb closes #99")
	assert.Contains(t, refused.Lines, "  commit bbbbbbbbb closes other/place#7")

	passed := worded(t, "Fixes https://github.com/Owner/Name/issues/12", "").run(audited(12))

	assert.Equal(t, merge.Done, passed.Stop)
}

func TestCommitKeywords_APlainNumberWithNoKeywordPasses(t *testing.T) {
	t.Parallel()

	result := worded(t, "Follow up of #99", "See #98, prefix #97, and the fix for #96").run(audited(12))

	assert.Equal(t, merge.Done, result.Stop)
}

func TestCommitKeywords_AnIssueTheOwnerKeptPasses(t *testing.T) {
	t.Parallel()

	req := audited(12)
	req.Kept = []model.IssueRef{"#99"}
	result := worded(t, "Tidy", "Fixes #99").run(req)

	assert.Equal(t, merge.Done, result.Stop)
}

func TestCommitKeywords_AnIssueOfAnotherRepositoryPassesOnlyWhenTheOwnerKeptItAsAFullReference(t *testing.T) {
	t.Parallel()

	req := audited(12)
	req.Kept = []model.IssueRef{"#7", "other/place#7"}
	kept := worded(t, "Tidy", "Fixes Other/Place#7").run(req)

	assert.Equal(t, merge.Done, kept.Stop)

	req.Kept = []model.IssueRef{"#7"}
	refused := worded(t, "Tidy", "Fixes other/place#7").run(req)

	assert.Equal(t, merge.Refused, refused.Stop, "the own issue #7 is not the issue 7 of another repository")
	assert.Contains(t, refused.Lines, "  commit bbbbbbbbb closes other/place#7")
}

func TestCommitKeywords_ANumberTooLargeForAnIssueRefuses(t *testing.T) {
	t.Parallel()

	result := worded(t, "Tidy", "Fixes #99999999999999999999999").run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines, "  commit bbbbbbbbb closes #99999999999999999999999")
}

func TestCommitKeywords_EveryOfferingCommitIsNamed(t *testing.T) {
	t.Parallel()

	w := listing(newWorld(t), 12)
	w.set(t, map[string]any{"commits": []any{commitJSON(dev+"@x", dev, "one", "Fixes #98"), commitJSON(dev+"@x", dev, "two", "Fixes #99")}})
	result := w.run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines, "  commit bbbbbbbbb closes #98")
	assert.Contains(t, result.Lines, "  commit bbbbbbbbb closes #99")
}

func TestCommitKeywords_ABaseThatIsNotTheDefaultBranchIsAuditedToo(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.closingRefs = &model.ClosingRefs{BaseBranch: "integration/r7", DefaultBranch: "main", Issues: []int{12}}
	w.set(t, map[string]any{"commits": []any{commitJSON(dev+"@x", dev, "Tidy", "Closes #99")}})
	result := w.run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines, "  commit bbbbbbbbb closes #99")
}

func TestCommitKeywords_ARequestThatNamesNoIssueRefusesAKeywordInACommit(t *testing.T) {
	t.Parallel()

	w := worded(t, "Tidy", "Fixes #99")
	w.closingRefs = &model.ClosingRefs{BaseBranch: "main", DefaultBranch: "main", Issues: []int{}}
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines, "  commit bbbbbbbbb closes #99")
}

func TestCommitKeywords_ACutReadOfTheCommitsRefusesAndNeverPasses(t *testing.T) {
	t.Parallel()

	w := worded(t, "Tidy", "Fixes #99")
	w.fails["PullCounts"] = true
	result := w.run(audited(12))

	assert.Equal(t, merge.OwnWork, result.Stop)
	assert.Contains(t, last(result), "cannot be counted")
	assert.Empty(t, merges(w))
}
