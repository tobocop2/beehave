package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
)

// emptyTree is the id of the tree with no file, which every git repository knows.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// foldOf checks out, detached, the commit that merges the heads of the items into main in order, as a library makes the union it gates.
func (r *batchRig) foldOf(items ...string) {
	r.t.Helper()

	prev := r.repo.must(r.t, "rev-parse", "main")
	for _, item := range items {
		tree := r.repo.must(r.t, "merge-tree", "--write-tree", prev, r.heads[item])
		prev = r.repo.must(r.t, "commit-tree", tree, "-p", prev, "-p", r.heads[item], "-m", "union")
	}

	r.repo.must(r.t, "switch", "-q", "--detach", prev)
}

// union writes a gate call, then one merge call of the members of the items with that gate. The report command shows the lists of the result.
func (r *batchRig) union(items, extra string) string {
	var members []string

	for _, item := range strings.Fields(items) {
		members = append(members, `struct(item = "`+item+`", head = "`+r.heads[item]+`", closes = [])`)
	}

	return `g = gate(name = "g")
out = merge(members = [` + strings.Join(members, ", ") + `], base = "main", repo = "o/r", gate = g, ` + extra + `name = "u")
command(name = "report", run = ["report", ",".join(out.merged), ",".join([r.item + ":" + r.reason for r in out.rejected]), ",".join([d.item + ":" + d.reason for d in out.deferred]), ",".join(out.rest), str(out.moved)])
`
}

// goldenUnionSteps are the step names of one merge call that lands a union of two members: the plan, then the intent of each member, then the tip.
// The forge merge of each member runs in the merge run, so the run of the call records no step for it.
var goldenUnionSteps = []string{"merge:u.plan", "merge:u.intent#1", "merge:u.intent#3", "merge:u.tip"}

func TestMerge_AUnionTakesThePlanThenTheStepsOfEachMemberThenTheTip(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1", "3")
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.union("1 3", ""))), modeOff())
	require.NoError(t, err)

	var recorded []string

	for _, name := range stepNamesOf(t, engine, testRun) {
		if strings.HasPrefix(name, "merge:") {
			recorded = append(recorded, name)
		}
	}

	assert.False(t, unpatchedChange(goldenUnionSteps, recorded), "the steps of a union: %v", recorded)
}

func TestMerge_AUnionGatedOnceLandsEveryMemberInOrderWithOneGate(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1", "3")

	_, err := rig.walk(t, rig.union("1 3", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed(), "both members land, in the given order")
	assert.Len(t, rig.gate.ran(), 1, "one gate for the whole union")
	assert.Equal(t, []string{"beehave/gate success " + rig.heads["1"], "beehave/gate success " + rig.heads["3"]}, rig.merger.statusesGiven())
	assert.Equal(t, []string{"report 1,3    False"}, filterReport(rig.h.commands.ran()))
	require.Len(t, rig.merger.given, 2)
	assert.Equal(t, rig.repo.must(t, "rev-parse", "HEAD^{tree}"), rig.merger.given[1].GatedTree, "the last member proves the tree that the gate ran on")
	assert.NotEqual(t, rig.merger.given[0].GatedTree, rig.merger.given[1].GatedTree, "the first member proves its own prefix tree")
}

func TestMerge_AGateOnACommitThatHoldsAMemberButNotItsCodePostsNoStatus(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	clean := rig.repo.must(t, "rev-parse", "main^{tree}")
	forged := rig.repo.must(t, "commit-tree", clean, "-p", "main", "-p", rig.heads["2"], "-m", "union")
	rig.repo.must(t, "switch", "-q", "--detach", forged)

	_, err := rig.walk(t, rig.union("2", ""))

	require.NoError(t, err)
	assert.Empty(t, rig.merger.statusesGiven(), "the gate ran on a tree without bad.txt, so no status names the head of item 2")
	assert.Empty(t, rig.merger.landed())
	assert.Equal(t, []string{"report     True"}, filterReport(rig.h.commands.ran()))
}

func TestMerge_AUnionWhoseFoldIsNotTheGatedTreeMergesNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1")

	body := rig.union("1 3", "") + `command(name = "report", run = ["report", out.reason])
`
	_, err := rig.walk(t, body)

	require.NoError(t, err)
	assert.Empty(t, rig.merger.landed())
	assert.Empty(t, rig.merger.statusesGiven())
	assert.Empty(t, rig.host.intents, "no pull request is read for a union that the gate did not see")
	lines := filterReport(rig.h.commands.ran())
	require.Len(t, lines, 2)
	assert.Equal(t, "report     True", lines[0])
	assert.Contains(t, lines[1], "and the gate ran on "+rig.repo.must(t, "rev-parse", "HEAD^{tree}")+". Gate the union of these members on this tip")
}

func TestMerge_ARefusalInsideTheUnionStopsTheLoopAndReturnsTheRest(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1", "3")
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Reason: merge.Plain, Lines: []string{"REFUSE: #1 carries the label hold", "REFUSE: second line"}, Refusal: []string{"REFUSE: #1 carries the label hold", "REFUSE: second line"}}}

	_, err := rig.walk(t, rig.union("1 3", `hold = "hold", `))

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the merge of item 1 was asked and refused; item 3 is never asked")
	assert.Equal(t, []string{"report  1:REFUSE: #1 carries the label hold\nREFUSE: second line  3 False"}, filterReport(rig.h.commands.ran()))
	assert.Equal(t, "hold", rig.merger.given[0].Hold)
}

func TestMerge_ATipThatMovesBetweenTwoMembersStopsTheCallAndDefersTheRest(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1", "3")
	rig.merger.after = func(item string) {
		if item == "1" {
			moved := rig.repo.must(t, "commit-tree", emptyTree, "-p", "main", "-m", "someone else landed")
			rig.repo.must(t, "update-ref", "refs/heads/main", moved)
		}
	}

	_, err := rig.walk(t, rig.union("1 3", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Equal(t, []string{"report 1  3:the base moved while the batch ran; run the batch again  False"}, filterReport(rig.h.commands.ran()))
}
