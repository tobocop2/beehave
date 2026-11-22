package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goldenOneMemberSteps are the step names of one merge call that lands one pull request. The call takes the steps of a merge of several pull requests,
// less the steps of the members that it does not hold.
var goldenOneMemberSteps = []string{
	"batch:b.refuse", "batch:b.heads#1", "batch:b.tip#1", "batch:b.union#1", "batch:b.tip#2", "batch:b.scope#1", "batch:b.intent#1", "batch:b.tip#3", "batch:b.prove", "batch:b.issues",
}

func TestMerge_AMergeOfOnePullRequestTakesTheStepsOfAMergeOfSeveral(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.call("1", ""))))
	require.NoError(t, err)

	var recorded []string

	for _, name := range stepNamesOf(t, engine, testRun) {
		if strings.HasPrefix(name, "batch:") {
			recorded = append(recorded, name)
		}
	}

	assert.False(t, unpatchedChange(goldenOneMemberSteps, recorded), "one pull request takes the same steps as several, one scope check, one intent and one merge for each member")
	assert.Equal(t, []string{"report 1  "}, rig.h.commands.ran(), "the one member is merged and nothing is rejected or deferred")
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Equal(t, "README\none.txt", rig.mainFiles(t), "main holds the one member")
	assert.Equal(t, []string{"1"}, rig.host.intents, "the intent of the member is written before its merge")

	require.Len(t, rig.merger.given, 1)
	assert.NotEmpty(t, rig.merger.given[0].GatedTree, "the merge compares the base tree with the gated tree")
	require.NotNil(t, rig.merger.given[0].Intent, "the merge carries the intent that was written before it")
}

func TestMerge_AMergeOfOnePullRequestWithARedGateRejectsItAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, rig.call("2", ""))
	require.NoError(t, err)

	assert.Equal(t, []string{"report  2:red "}, rig.h.commands.ran())
	assert.Empty(t, rig.merger.landed(), "the red member is not merged")
}
