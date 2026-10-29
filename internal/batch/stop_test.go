package batch

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func refused(reason string) MergeOutcome { return MergeOutcome{Status: StatusRefused, Reason: reason} }

func unsettled(reason string) MergeOutcome {
	return MergeOutcome{Status: StatusUnsettled, Reason: reason}
}

func TestRun_AMemberRefusalRejectsThatMemberAndGatesTheRestAgain(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.answers = map[string]MergeOutcome{"b": refused("b is own work")}

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c", "d"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "c", "d"}, res.Merged)
	assert.Equal(t, []Outcome{{Item: "b", Reason: "b is own work"}}, res.Rejected)
	assert.Empty(t, res.Deferred)
	assert.Equal(t, 2, res.Gates)
	assert.Equal(t, []string{"t:a,b,c,d", "t:a,c,d"}, w.gateTrees)
	assert.Equal(t, "t:a,c,d", res.Tip.Tree)
}

func TestRun_ARefusalOfTheLastMemberNeedsNoNewGate(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.answers = map[string]MergeOutcome{"b": refused("hold label")}

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, res.Merged)
	assert.Equal(t, []Outcome{{Item: "b", Reason: "hold label"}}, res.Rejected)
	assert.Equal(t, 1, res.Gates)
}

func TestRun_ARefusalOfTheFirstMemberGatesTheRestOnTheSameTip(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.answers = map[string]MergeOutcome{"a": refused("review missing")}

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, res.Merged)
	assert.Equal(t, []Outcome{{Item: "a", Reason: "review missing"}}, res.Rejected)
	assert.Equal(t, []string{"t:a,b", "t:b"}, w.gateTrees)
}

func TestRun_AnUnknownAnswerOfTheForgeStopsTheCallAtThatMember(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.answers = map[string]MergeOutcome{"b": {Status: MergeStatus(9)}}

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.ErrorContains(t, err, "merge b: the forge answered with the unknown status 9")
	assert.Equal(t, []string{"a"}, res.Merged)
}

func TestRun_AnUnsettledAnswerDefersThatMemberAndTheLaterOnes(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.answers = map[string]MergeOutcome{"b": unsettled("mergeable stayed unknown")}

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, res.Merged)
	assert.Empty(t, res.Rejected)
	assert.Equal(t, []Outcome{{Item: "b", Reason: "mergeable stayed unknown"}, {Item: "c", Reason: "mergeable stayed unknown"}}, res.Deferred)
	assert.Equal(t, 1, res.Gates)
	assert.Equal(t, []string{"b"}, w.asked)
}

func TestRun_AnUnsettledAnswerInsideASplitDefersTheOtherHalfWithTheSameReason(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.bad = func(items []string) bool { return has(items, "d") }
	w.answers = map[string]MergeOutcome{"b": unsettled("mergeable stayed unknown")}

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c", "d"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, res.Merged)
	assert.Equal(t, []Outcome{
		{Item: "b", Reason: "mergeable stayed unknown"},
		{Item: "c", Reason: "mergeable stayed unknown"},
		{Item: "d", Reason: "mergeable stayed unknown"},
	}, res.Deferred)
}

func TestRun_AnErrorAfterMergesStillListsThemAndNamesTheMember(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.failMerge, w.failMergeItem = errBoom, "c"

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c", "d"))

	require.ErrorIs(t, err, errBoom)
	require.ErrorContains(t, err, "merge c: boom")
	assert.Equal(t, []string{"a", "b"}, res.Merged)
	assert.Equal(t, 1, res.Gates)
	assert.Equal(t, "t:a,b", res.Tip.Tree)
}

func TestRun_ATipReadErrorBeforeAMergeNamesTheMember(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.store.failOn["batch:q.tip#3"] = errBoom

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.ErrorContains(t, err, "before merging b: batch:q.tip#3: boom")
	assert.Equal(t, []string{"a"}, res.Merged)
}

func TestRun_AnUnfetchedHeadIsRejectedBeforeTheFold(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.missing = map[string]bool{"h-b": true}

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "c"}, res.Merged)
	assert.Equal(t, []Outcome{{Item: "b", Reason: "head h-b of b is not in the repository; fetch it"}}, res.Rejected)
	assert.Equal(t, []string{"t:a,c"}, w.gateTrees)
	assert.Equal(t, 2, w.mergeTreeCalls)
}

func TestRun_EveryHeadUnfetchedGatesNothing(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.missing = map[string]bool{"h-a": true}

	res, err := Run(context.Background(), w.deps(), members("a"))

	require.NoError(t, err)
	assert.Empty(t, res.Merged)
	assert.Len(t, res.Rejected, 1)
	assert.Zero(t, res.Gates)
	assert.Zero(t, w.mergeTreeCalls)
}

func TestRun_TheHeadCheckIsARecordedStep(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.missing = map[string]bool{"h-b": true}

	_, err := Run(context.Background(), w.deps(), members("a", "b"))
	require.NoError(t, err)

	w.missing = nil
	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.NoError(t, err)
	assert.Equal(t, []Outcome{{Item: "b", Reason: "head h-b of b is not in the repository; fetch it"}}, res.Rejected)
}

func TestRun_AHeadCheckErrorStopsTheCall(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.failHasCommit = errBoom

	_, err := Run(context.Background(), w.deps(), members("a"))

	require.ErrorIs(t, err, errBoom)
	assert.Zero(t, w.mergeTreeCalls)
}

func TestRun_RefusesAnEmptyBatchNameBeforeAnyCall(t *testing.T) {
	t.Parallel()

	w := newWorld()
	deps := w.deps()
	deps.Name = ""

	_, err := Run(context.Background(), deps, members("a"))

	require.EqualError(t, err, "a batch needs a name that is not empty and differs from every other batch call in the workflow")
	assert.Empty(t, w.store.names)
}

// TestRun_TheRetryOfHeldMembersRunsOnce: a is red, so b and d are held. In the retry pass d conflicts with b and is rejected.
// A second retry would land d, because b is red. The exact steps show that the retry runs once.
func TestRun_TheRetryOfHeldMembersRunsOnce(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.conflict = func(have []string, item string) bool {
		return (item == "b" && has(have, "a")) || (item == "d" && (has(have, "a") || has(have, "b")))
	}
	w.bad = func(items []string) bool { return has(items, "a") || has(items, "b") }

	res, err := Run(context.Background(), w.deps(), members("a", "b", "d"))

	require.NoError(t, err)
	assert.Empty(t, res.Merged)
	assert.Equal(t, []Outcome{
		{Item: "a", Reason: ReasonRed, Report: "red on t:a"},
		{Item: "d", Reason: "conflicts with b"},
		{Item: "b", Reason: ReasonRed, Report: "red on t:b"},
	}, res.Rejected)
	assert.Equal(t, 2, res.Gates)
	assert.Equal(t, []string{
		"batch:q.heads#1", "batch:q.tip#1",
		"batch:q.union#1", "batch:q.gate#1",
		"batch:q.union#2", "batch:q.gate#2",
	}, w.store.names)
}

// TestRun_GateCountWithRetryAcrossN: m00 is red and the next H members conflict with it, so they are held out for the retry.
// Round one has N-H members with one bad member. The retry has H members and none bad.
func TestRun_GateCountWithRetryAcrossN(t *testing.T) {
	t.Parallel()

	for n := 3; n <= 16; n++ {
		for held := 1; held < n; held++ {
			t.Run(fmt.Sprintf("N=%d H=%d", n, held), func(t *testing.T) {
				t.Parallel()

				names := make([]string, 0, n)
				for i := range n {
					names = append(names, fmt.Sprintf("m%02d", i))
				}

				heldOut := names[1 : 1+held]
				w := newWorld()
				w.conflict = func(have []string, item string) bool { return has(heldOut, item) && has(have, "m00") }
				w.bad = func(items []string) bool { return has(items, "m00") }

				res, err := Run(context.Background(), w.deps(), members(names...))

				require.NoError(t, err)
				assert.Len(t, res.Merged, n-1)
				assert.Equal(t, []string{"m00"}, itemsOfOutcomes(res.Rejected))
				assert.LessOrEqual(t, res.Gates, Bound(n-held, 1)+Bound(held, 0))
				assert.LessOrEqual(t, res.Gates, 2*n-2)
			})
		}
	}
}

func TestGitCLI_HasCommit(t *testing.T) {
	t.Parallel()

	s, g := scripted(
		model.CommandResult{},
		model.CommandResult{ExitCode: 1},
		model.CommandResult{ExitCode: 128, Stderr: "fatal: Not a valid object name\n"},
		model.CommandResult{ExitCode: 2, Stderr: "boom\n"},
	)
	ctx := context.Background()

	found, err := g.HasCommit(ctx, "abc")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []string{"cat-file", "-e", "abc^{commit}"}, s.calls[0])

	found, err = g.HasCommit(ctx, "abc")
	require.NoError(t, err)
	assert.False(t, found)

	found, err = g.HasCommit(ctx, "abc")
	require.NoError(t, err)
	assert.False(t, found)

	_, err = g.HasCommit(ctx, "abc")
	require.EqualError(t, err, "git cat-file failed with exit code 2: boom")
}

func TestRun_RealGitRejectsAHeadThatIsNotInTheRepository(t *testing.T) {
	t.Parallel()

	g := newRepo(t)
	x := g.commitOn(t, "x", "x.txt", "x\n")
	g.must(t, "switch", "-q", "main")

	tip := Tip{Commit: g.must(t, "rev-parse", "HEAD"), Tree: g.must(t, "rev-parse", "HEAD^{tree}")}
	unknown := strings.Repeat("1234567890", 4)
	deps := Deps{Git: GitCLI{Exec: g}, Gate: &fixedGate{}, Base: &fixedBase{tip: tip}, Steps: newMemory(), Name: "real"}

	res, err := Run(context.Background(), deps, []Member{{Item: "u", Head: unknown}, {Item: "x", Head: x}})

	require.NoError(t, err)
	assert.Equal(t, []string{"x"}, res.Merged)
	assert.Equal(t, []Outcome{{Item: "u", Reason: "head " + unknown + " of u is not in the repository; fetch it"}}, res.Rejected)
}
