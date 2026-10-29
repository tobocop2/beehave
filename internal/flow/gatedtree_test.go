package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/review"
)

const rewrittenHead = "89abcdef0123456789abcdef0123456789abcdef"

func TestGateStep_TheTreeOfTheGatedHeadIsAnOutputOfTheStep(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1"}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\ncommand(run = [\"show\", c.tree])\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{"show tree-1"}, h.commands.ran())
}

func TestGateStep_AGateProgramRecordsNoTree(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, bodyFlow("c = gate(run = [\"true\"], name = \"check\")\ncommand(run = [\"show\", \"[\" + c.tree + \"]\"])\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{"true", "show []"}, h.commands.ran())
}

func TestMergeStep_TheTreeThatTheGateRecordedReachesTheMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1"}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\nmerge(head = c.head, tree = c.tree, merge = True, name = \"ship\")\n"))

	require.NoError(t, err)
	require.Len(t, h.merge.given, 1)
	assert.Equal(t, "tree-1", h.merge.given[0].GatedTree)
}

func TestMergeStep_ACallWithoutATreeAsksForNoProof(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, shipFlow("merge = True, "))

	require.NoError(t, err)
	require.Len(t, h.merge.given, 1)
	assert.Empty(t, h.merge.given[0].GatedTree)
}

func TestMergeStep_AnExplicitEmptyTreeIsRefusedBeforeAnyMergeAndTheMessageSaysWhy(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"a literal": shipFlow(`merge = True, tree = "", `),
		"the tree of a gate that runs a program": bodyFlow("c = gate(run = [\"true\"], name = \"check\")\n" +
			"merge(head = \"" + gatedHead + "\", tree = c.tree, merge = True, name = \"ship\")\n"),
	} {
		h := mergeHarness(t, merge.Result{Stop: merge.Done})

		_, err := walkText(t, h, text)

		require.ErrorContains(t, err, "`tree` is empty. The gate recorded no tree for this head", name)
		require.ErrorContains(t, err, "Leave out `tree` to merge without the proof", name)
		assert.Empty(t, h.merge.given, name)
		assert.Empty(t, h.merge.asked, name)
	}
}

func TestMergeStep_AnEmptyTreeIsNotReadWhenTheCallDoesNotMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, shipFlow(`tree = "", `))

	require.NoError(t, err)
	assert.Empty(t, h.merge.given)
}

func TestMergeStep_AGateThatRecordedNoTreeDoesNotStopACallThatDoesNotMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, bodyFlow("c = gate(run = [\"true\"], name = \"check\")\n"+
		"merge(head = \""+gatedHead+"\", tree = c.tree, name = \"ship\")\n"))

	require.NoError(t, err)
}

func TestMergeStep_AReviewOfTheOldHeadStandsForARewriteWithTheSameTreeAndTheMergePinsTheNewHead(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.state = review.Standing{Number: testPull, Head: rewrittenHead}
	h.forge.rewritten = true

	_, err := walkText(t, h, shipFlow("merge = True, "))

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + " " + testItem + " " + rewrittenHead}, h.merge.asked)
}

func TestMergeStep_AReviewOfTheOldHeadDoesNotStandForAHeadWithAnotherTree(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.forge.state = review.Standing{Number: testPull, Head: rewrittenHead}

	_, err := walkText(t, h, shipFlow("merge = True, "))

	require.ErrorIs(t, err, errNotReady)
	assert.Empty(t, h.merge.asked)
}
