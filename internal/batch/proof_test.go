package batch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/batch"
	"github.com/tobocop2/obee/internal/model"
)

var errRead = errors.New("the forge is down")

// proofWorld is a forge that answers the comparison of each head, the tree of each commit and the files of each tree.
type proofWorld struct {
	compare    map[string]model.Comparison
	compareErr map[string]error
	trees      map[string]string
	blobs      map[string]map[string]string
	blobsErr   map[string]error
	treeErr    map[string]error
	asked      []string
	// branch is the current tip of the branch of each item. An item with no entry has no branch. branchErr fails the read of an item.
	branch    map[string]string
	branchErr map[string]error
}

func (w *proofWorld) BranchRef(_ context.Context, branch string) (string, bool, error) {
	w.asked = append(w.asked, "branch "+branch)

	tip, found := w.branch[branch]

	return tip, found, w.branchErr[branch]
}

func (w *proofWorld) Compare(_ context.Context, commit, base string) (model.Comparison, error) {
	w.asked = append(w.asked, "compare "+commit+" "+base)

	return w.compare[commit], w.compareErr[commit]
}

func (w *proofWorld) CommitTree(_ context.Context, commit string) (string, error) {
	w.asked = append(w.asked, "tree "+commit)

	if err := w.treeErr[commit]; err != nil {
		return "", err
	}

	tree, ok := w.trees[commit]
	if !ok {
		return "", errRead
	}

	return tree, nil
}

func (w *proofWorld) TreeBlobs(_ context.Context, tree string) (map[string]string, error) {
	w.asked = append(w.asked, "blobs "+tree)

	return w.blobs[tree], w.blobsErr[tree]
}

var (
	startTip = batch.Tip{Commit: "start", Tree: "t-start"}
	endTip   = batch.Tip{Commit: "end", Tree: "t-end"}
	onBase   = model.Comparison{Status: "ahead", AheadBy: 5}
	offBase  = model.Comparison{Status: "diverged", AheadBy: 5, BehindBy: 2}
)

// world gives a member b whose head changes a.go and removes old.go against the start tip, and a base that holds the same files after the batch.
func newProofWorld() *proofWorld {
	return &proofWorld{
		compare: map[string]model.Comparison{"h-a": onBase, "h-b": offBase},
		branch:  map[string]string{"a": "h-a", "b": "h-b", "c": "h-c"},
		trees:   map[string]string{"h-b": "t-b"},
		blobs: map[string]map[string]string{
			"t-start": {"a.go": "1", "old.go": "9", "keep.go": "5"},
			"t-b":     {"a.go": "2", "keep.go": "5"},
			"t-end":   {"a.go": "2", "keep.go": "5", "z.go": "7"},
		},
	}
}

func members() []batch.Member {
	return []batch.Member{{Item: "a", Head: "h-a"}, {Item: "b", Head: "h-b"}}
}

func TestProve_AMemberWhoseHeadIsOnTheBaseIsLandedWithoutAnyTreeRead(t *testing.T) {
	t.Parallel()

	w := newProofWorld()
	got := batch.Prove(t.Context(), w, "main", startTip, endTip, members()[:1])

	assert.Empty(t, got)
	assert.Equal(t, []string{"branch a", "compare h-a main"}, w.asked)
}

func TestProve_AMemberWhoseBranchWasPushedAfterTheFoldIsUnprovenEvenWhenTheFoldedHeadIsOnTheBase(t *testing.T) {
	t.Parallel()

	w := newProofWorld()
	w.branch["a"] = "h-a-forced" // h-a is on the base, and the branch no longer holds it

	got := batch.Prove(t.Context(), w, "main", startTip, endTip, members()[:1])

	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Item)
	assert.Contains(t, got[0].Reason, "h-a-forced")
	assert.Contains(t, got[0].Reason, "folded h-a")
	assert.NotContains(t, w.asked, "compare h-a main", "the old head is not compared: the forge still holds it")
}

func TestProve_ABranchThatCannotBeReadLeavesTheMemberUnproven(t *testing.T) {
	t.Parallel()

	w := newProofWorld()
	w.branchErr = map[string]error{"a": errRead}

	got := batch.Prove(t.Context(), w, "main", startTip, endTip, members()[:1])

	require.Len(t, got, 1)
	assert.Contains(t, got[0].Reason, "cannot read its branch")
}

func TestProve_ABranchThatIsGoneHasNoNewerCommitAndTheCompareDecides(t *testing.T) {
	t.Parallel()

	w := newProofWorld()
	delete(w.branch, "a")

	assert.Empty(t, batch.Prove(t.Context(), w, "main", startTip, endTip, members()[:1]))
}

func TestProve_AMemberWhoseHeadIsNotOnTheBaseAndNotOnTheVehicleIsUnprovenAndNeverLanded(t *testing.T) {
	t.Parallel()

	w := newProofWorld()
	w.blobs["t-end"]["a.go"] = "3" // the base holds another content of a.go than the member

	got := batch.Prove(t.Context(), w, "main", startTip, endTip, members())

	require.Len(t, got, 1)
	assert.Equal(t, "b", got[0].Item)
	assert.Contains(t, got[0].Reason, "h-b")
	assert.Contains(t, got[0].Reason, "a.go")
	assert.Contains(t, got[0].Reason, "ahead by 5, behind by 2", "the reason prints both counts")
}

func TestProve_AMemberOnTheVehicleByBlobAfterASquashLands(t *testing.T) {
	t.Parallel()

	w := newProofWorld()
	got := batch.Prove(t.Context(), w, "main", startTip, endTip, members())

	assert.Empty(t, got, "every path the member changed holds the same content on the base, so the member is on the vehicle")
}

func TestProve_TwoMembersOffTheBaseAreCheckedAgainstTheSameTwoTreesReadOnce(t *testing.T) {
	t.Parallel()

	w := newProofWorld()
	w.compare["h-c"] = offBase
	w.trees["h-c"] = "t-c"
	w.blobs["t-c"] = map[string]string{"a.go": "4", "old.go": "9", "keep.go": "5"} // the base does not hold content 4 of a.go

	got := batch.Prove(t.Context(), w, "main", startTip, endTip, append(members(), batch.Member{Item: "c", Head: "h-c"}))

	require.Len(t, got, 1, "b is on the vehicle and c is not")
	assert.Equal(t, "c", got[0].Item)

	reads := 0

	for _, ask := range w.asked {
		if ask == "blobs t-start" || ask == "blobs t-end" {
			reads++
		}
	}

	assert.Equal(t, 2, reads, "the start tree and the end tree are listed once for the whole batch")
}

func TestProve_AFileTheMemberRemovedThatTheBaseStillHoldsIsUnproven(t *testing.T) {
	t.Parallel()

	w := newProofWorld()
	w.blobs["t-end"]["old.go"] = "9"

	got := batch.Prove(t.Context(), w, "main", startTip, endTip, members())

	require.Len(t, got, 1)
	assert.Contains(t, got[0].Reason, "old.go")
}

func TestProve_AMemberThatChangesNothingAgainstTheStartTipAndIsNotOnTheBaseIsUnproven(t *testing.T) {
	t.Parallel()

	w := newProofWorld()
	w.blobs["t-b"] = w.blobs["t-start"]

	got := batch.Prove(t.Context(), w, "main", startTip, endTip, members())

	require.Len(t, got, 1)
	assert.Contains(t, got[0].Reason, "changes nothing")
}

func TestProve_AReadThatFailsLeavesTheMemberUnprovenNeverLanded(t *testing.T) {
	t.Parallel()

	tests := map[string]func(w *proofWorld){
		"compare": func(w *proofWorld) { w.compareErr = map[string]error{"h-b": errRead} },
		"commit":  func(w *proofWorld) { w.treeErr = map[string]error{"h-b": errRead} },
		"member":  func(w *proofWorld) { w.blobsErr = map[string]error{"t-b": errRead} },
		"start":   func(w *proofWorld) { w.blobsErr = map[string]error{"t-start": errRead} },
		"end":     func(w *proofWorld) { w.blobsErr = map[string]error{"t-end": errRead} },
	}

	for name, fail := range tests {
		w := newProofWorld()
		fail(w)

		got := batch.Prove(t.Context(), w, "main", startTip, endTip, members())

		require.Len(t, got, 1, name)
		assert.Contains(t, got[0].Reason, "cannot", name)
		assert.Equal(t, "b", got[0].Item, name)
	}
}

func TestProve_AnEmptyListOfMembersProvesNothingAndReadsNothing(t *testing.T) {
	t.Parallel()

	w := newProofWorld()

	assert.Empty(t, batch.Prove(t.Context(), w, "main", startTip, endTip, nil))
	assert.Empty(t, w.asked)
}
