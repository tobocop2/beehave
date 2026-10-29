package batch

import (
	"context"
	"fmt"
	"math/bits"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const maxBad = 3

func names(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("p%02d", i))
	}

	return out
}

func TestBound_Values(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, Bound(0, 1))
	assert.Equal(t, 1, Bound(1, 1))
	assert.Equal(t, 1, Bound(16, 0))
	assert.Equal(t, 9, Bound(16, 1))
	assert.Equal(t, 15, Bound(8, 3))
}

// TestRun_BisectEveryBadSubset runs N from 1 to 16 and every set of up to three bad members. The gate is red when the union holds a bad member.
func TestRun_BisectEveryBadSubset(t *testing.T) {
	t.Parallel()

	for n := 1; n <= 16; n++ {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			t.Parallel()

			all := names(n)

			for mask := range 1 << n {
				if bits.OnesCount(uint(mask)) <= maxBad {
					bad, good := split(all, mask)
					checkBisect(t, all, bad, good)
				}
			}
		})
	}
}

// split divides the items by the bits of mask: a set bit is a bad member.
func split(all []string, mask int) (bad, good []string) {
	for i, item := range all {
		if mask>>i&1 == 1 {
			bad = append(bad, item)
		} else {
			good = append(good, item)
		}
	}

	return bad, good
}

func checkBisect(t *testing.T, all, bad, good []string) {
	t.Helper()

	w := newWorld()
	w.bad = func(items []string) bool {
		return slices.ContainsFunc(items, func(item string) bool { return has(bad, item) })
	}

	res, err := Run(context.Background(), w.deps(), members(all...))
	require.NoError(t, err)

	require.Equal(t, bad, itemsOfOutcomes(res.Rejected), "bad %v", bad)
	require.Equal(t, good, res.Merged, "bad %v", bad)
	require.Empty(t, res.Deferred)
	require.LessOrEqual(t, res.Gates, Bound(len(all), len(bad)), "n=%d bad %v", len(all), bad)
	require.Len(t, w.gateTrees, res.Gates)
}

func TestRun_AllGreenCostsOneGate(t *testing.T) {
	t.Parallel()

	w := newWorld()
	res, err := Run(context.Background(), w.deps(), members("b", "a", "c"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, res.Merged)
	assert.Equal(t, 1, res.Gates)
	assert.Equal(t, Tip{Commit: "c:a,b,c", Tree: "t:a,b,c"}, res.Tip)
}

func TestRun_EmptyBatchGatesNothing(t *testing.T) {
	t.Parallel()

	w := newWorld()
	res, err := Run(context.Background(), w.deps(), nil)

	require.NoError(t, err)
	assert.Empty(t, res.Merged)
	assert.Zero(t, res.Gates)
}

// TestRun_MemoSkipsAGateOnATreeAlreadyRed: a is green after the pair was red, so the union of b on the new tip is the tree of the red pair.
func TestRun_MemoSkipsAGateOnATreeAlreadyRed(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.bad = func(items []string) bool { return has(items, "b") }

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, res.Merged)
	assert.Equal(t, 2, res.Gates)
	assert.Equal(t, []string{"t:a,b", "t:a"}, w.gateTrees)
	require.Len(t, res.Rejected, 1)
	assert.Equal(t, "b", res.Rejected[0].Item)
	assert.Equal(t, ReasonRedOnTop, res.Rejected[0].Reason)
	assert.Equal(t, "red on t:a,b", res.Rejected[0].Report)
}

func TestRun_InteractionRejectsTheLaterMember(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.bad = func(items []string) bool { return has(items, "a") && has(items, "c") }

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, res.Merged)
	assert.Equal(t, []string{"c"}, itemsOfOutcomes(res.Rejected))
	assert.Equal(t, ReasonRedOnTop, res.Rejected[0].Reason)
}

func TestRun_SingleRedMemberKeepsTheRedReason(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.bad = func(items []string) bool { return has(items, "a") }

	res, err := Run(context.Background(), w.deps(), members("a"))

	require.NoError(t, err)
	assert.Equal(t, ReasonRed, res.Rejected[0].Reason)
	assert.Equal(t, 1, res.Gates)
}

func TestRun_ConflictWithTheBaseIsRejectedBeforeAnyGate(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = func(_ []string, item string) bool { return item != "ok" }

	res, err := Run(context.Background(), w.deps(), members("x", "y"))

	require.NoError(t, err)
	assert.Empty(t, res.Merged)
	assert.Equal(t, []string{"x", "y"}, itemsOfOutcomes(res.Rejected))
	assert.Equal(t, ReasonConflict, res.Rejected[0].Reason)
	assert.Zero(t, res.Gates)
	assert.Empty(t, w.gateTrees)
}

// pairConflict makes b conflict with a.
func pairConflict(have []string, item string) bool { return item == "b" && has(have, "a") }

func TestRun_HeldMemberLandsWhenTheEarlierMemberWasRejectedRed(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = pairConflict
	w.bad = func(items []string) bool { return has(items, "a") }

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, res.Merged)
	assert.Equal(t, []string{"a"}, itemsOfOutcomes(res.Rejected))
	assert.Equal(t, 2, res.Gates)
}

func TestRun_HeldMemberIsRejectedWhenTheEarlierMemberLanded(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = pairConflict

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, res.Merged)
	require.Len(t, res.Rejected, 1)
	assert.Equal(t, Outcome{Item: "b", Reason: "conflicts with a"}, res.Rejected[0])
	assert.Equal(t, 1, res.Gates)
}

// TestRun_HeldMemberConflictsWithBaseWhenTheBlamedMemberDidNotLand: b conflicts with a and with c. a is red, c lands, and b meets c on the new tip.
func TestRun_HeldMemberConflictsWithBaseWhenTheBlamedMemberDidNotLand(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = func(have []string, item string) bool { return item == "b" && (has(have, "a") || has(have, "c")) }
	w.bad = func(items []string) bool { return has(items, "a") }

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c"))

	require.NoError(t, err)
	assert.Equal(t, []string{"c"}, res.Merged)
	assert.Equal(t, []Outcome{{Item: "a", Reason: ReasonRed, Report: "red on t:a"}, {Item: "b", Reason: "conflicts with base"}}, res.Rejected)
}

// TestRun_GateCountWithRetryStaysUnderTheSumOfTheTwoBounds: b and d are held out, a and c are red. Round one has 3 members and 2 bad, the retry has 2 members and none bad.
func TestRun_GateCountWithRetryStaysUnderTheSumOfTheTwoBounds(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = func(have []string, item string) bool {
		return (item == "b" && has(have, "a")) || (item == "d" && has(have, "c"))
	}
	w.bad = func(items []string) bool { return has(items, "a") || has(items, "c") }

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c", "d", "e"))

	require.NoError(t, err)
	assert.Equal(t, []string{"e", "b", "d"}, res.Merged)
	assert.Equal(t, []string{"a", "c"}, itemsOfOutcomes(res.Rejected))
	assert.LessOrEqual(t, res.Gates, Bound(3, 2)+Bound(2, 0))
	assert.Equal(t, 6, res.Gates)
}

// TestRun_RetryRejectsAHeldMemberThatConflictsWithAnotherRetriedMember: b and d are held by a, a is red, and in the retry d meets b.
func TestRun_RetryRejectsAHeldMemberThatConflictsWithAnotherRetriedMember(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = func(have []string, item string) bool {
		return (item == "b" && has(have, "a")) || (item == "d" && (has(have, "a") || has(have, "b")))
	}
	w.bad = func(items []string) bool { return has(items, "a") }

	res, err := Run(context.Background(), w.deps(), members("a", "b", "d"))

	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, res.Merged)
	assert.Equal(t, []Outcome{{Item: "a", Reason: ReasonRed, Report: "red on t:a"}, {Item: "d", Reason: "conflicts with b"}}, res.Rejected)
}

// TestRun_NoSecondRetry: d meets c in the first round and meets the landed c again in the retry pass, where it is rejected and not folded a third time.
func TestRun_NoSecondRetry(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = func(have []string, item string) bool { return item == "d" && has(have, "c") }

	res, err := Run(context.Background(), w.deps(), members("c", "d"))

	require.NoError(t, err)
	assert.Equal(t, []string{"c"}, res.Merged)
	assert.Equal(t, []Outcome{{Item: "d", Reason: "conflicts with c"}}, res.Rejected)
	assert.Equal(t, 1, res.Gates)
	assert.Equal(t, 2, w.store.countPrefix("batch:q.union#"))
}

// TestRun_SubsetConflictsAfterASplit: b needs a. When a is red and rejected, b meets the new tip alone and is rejected as a conflict.
func TestRun_SubsetConflictsAfterASplit(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = func(have []string, item string) bool { return item == "b" && !has(have, "a") }
	w.bad = func(items []string) bool { return has(items, "a") }

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.NoError(t, err)
	assert.Empty(t, res.Merged)
	assert.Equal(t, []Outcome{{Item: "a", Reason: ReasonRed, Report: "red on t:a"}, {Item: "b", Reason: ReasonConflict}}, res.Rejected)
}

// TestRun_SubsetHeldAfterASplitIsRetried: d conflicts with c unless a is in the union. Once a is rejected, d is held in the middle of round one and retried after it.
func TestRun_SubsetHeldAfterASplitIsRetried(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = func(have []string, item string) bool { return item == "d" && has(have, "c") && !has(have, "a") }
	w.bad = func(items []string) bool { return has(items, "a") }

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c", "d"))

	require.NoError(t, err)
	assert.Equal(t, []string{"b", "c"}, res.Merged)
	assert.Equal(t, []string{"a", "d"}, itemsOfOutcomes(res.Rejected))
	assert.Equal(t, "conflicts with c", res.Rejected[1].Reason)
}

func TestRun_BaseMovedDefersTheRest(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.pushAfter = 1

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, res.Merged)
	assert.Equal(t, []Outcome{{Item: "b", Reason: ReasonMoved}, {Item: "c", Reason: ReasonMoved}}, res.Deferred)
	assert.Equal(t, "t:a,other", res.Tip.Tree)
	assert.Equal(t, []string{"a"}, w.merges)
}

func TestRun_BaseMovedAfterTheLastMergeDoesNotDeferAnything(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.pushAfter = 2

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, res.Merged)
	assert.Empty(t, res.Deferred)
}

func TestRun_BaseMovedInsideASplitStopsTheOtherHalfAndTheRetry(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = pairConflict
	w.bad = func(items []string) bool { return has(items, "d") }
	w.pushAfter = 1

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c", "d"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, res.Merged)
	assert.Equal(t, []Outcome{{Item: "b", Reason: ReasonMoved}, {Item: "c", Reason: ReasonMoved}, {Item: "d", Reason: ReasonMoved}}, res.Deferred)
	assert.Empty(t, res.Rejected)
}

func TestRun_RefusesBadMembersBeforeAnyCall(t *testing.T) {
	t.Parallel()

	for name, list := range map[string][]Member{
		"no item": {{Head: "h-a"}},
		"no head": {{Item: "a"}},
		"twice":   members("a", "a"),
	} {
		w := newWorld()
		_, err := Run(context.Background(), w.deps(), list)

		require.Error(t, err, name)
		assert.Empty(t, w.store.names, name)
	}
}

func TestRun_OldGitFailsBeforeAnyStep(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.version = "git version 2.30.2"

	_, err := Run(context.Background(), w.deps(), members("a"))

	require.EqualError(t, err, "git 2.38 or newer is needed for batch_merge; found 2.30. Upgrade git on the gate host")
	assert.Empty(t, w.store.names)
}

func TestRun_SortsMembersByByteOrder(t *testing.T) {
	t.Parallel()

	w := newWorld()
	res, err := Run(context.Background(), w.deps(), members("b", "B", "a", "10", "9"))

	require.NoError(t, err)
	assert.Equal(t, []string{"10", "9", "B", "a", "b"}, res.Merged)
}
