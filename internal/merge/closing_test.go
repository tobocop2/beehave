package merge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

// audited is a request that names the given issues and turns the audit of the closing references on.
func audited(names ...int) merge.Request {
	req := request()
	req.AuditClosing = true
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
	assert.Contains(t, last(result), "#5 closes #12 #99 and the item names #12")
	assert.Empty(t, merges(w))
}

func TestClosing_AMissingIssueRefusesAndNamesBothLists(t *testing.T) {
	t.Parallel()

	w := listing(newWorld(t))
	result := w.run(audited(12))

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "#5 closes no issue and the item names #12")
	assert.Empty(t, merges(w))
}

func TestClosing_AForgeThatCannotReadClosingReferencesRefusesAndNeverPasses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
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

func TestClosing_ABaseThatIsNotTheDefaultBranchSkipsTheAuditAndSaysWhichBase(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.closingRefs = &model.ClosingRefs{BaseBranch: "integration/r7", DefaultBranch: "main"}
	result := w.run(audited(12))

	assert.Equal(t, merge.Done, result.Stop)
	assert.Contains(t, result.Lines, "closing references: #5 targets integration/r7, not the default branch main, so GitHub lists none and the audit is skipped.")
}

func TestClosing_WithTheAuditOffTheForgeIsNotAsked(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	result := w.run(request())

	assert.Equal(t, merge.Done, result.Stop)
	assert.NotContains(t, w.calls, "ClosingRefs")
}

func TestClosing_WithTheAuditOffTheMergeSaysWhyNoAuditRan(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	req := request()
	req.Unaudited = "closing references: the tracker local has no issue numbers, so no audit ran."
	result := w.run(req)

	assert.Equal(t, merge.Done, result.Stop)
	assert.Contains(t, result.Lines, "closing references: the tracker local has no issue numbers, so no audit ran.")
	assert.NotContains(t, w.calls, "ClosingRefs")
}
