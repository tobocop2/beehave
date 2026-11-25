package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goldenOneMemberSteps are the step names of one merge call that lands one pull request: the plan, the intent of the member, then the tip.
var goldenOneMemberSteps = []string{"merge:b.plan", "merge:b.intent#1", "merge:b.tip"}

func TestMerge_AMergeOfOnePullRequestTakesTheStepsOfAMergeOfSeveral(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.call("1", ""))), modeOff())
	require.NoError(t, err)

	var recorded []string

	for _, name := range stepNamesOf(t, engine, testRun) {
		if strings.HasPrefix(name, "merge:") {
			recorded = append(recorded, name)
		}
	}

	assert.False(t, unpatchedChange(goldenOneMemberSteps, recorded), "one pull request takes the same steps as several, one intent for each member: %v", recorded)
	assert.Equal(t, "report 1  ", rig.reportLine(t), "the one member is merged and nothing is rejected or deferred")
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Equal(t, "README\none.txt", rig.mainFiles(t), "main holds the one member")
	assert.Equal(t, []string{"1"}, rig.host.intents, "the intent of the member is written before its merge")

	require.Len(t, rig.merger.given, 1)
	assert.NotEmpty(t, rig.merger.given[0].GatedTree, "the merge compares the base tree with the gated tree")
	require.NotNil(t, rig.merger.given[0].Intent, "the merge carries the intent that was written before it")
}

func TestMerge_AMergeOfOnePullRequestWithARedGateLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, rig.call("2", ""))

	require.ErrorContains(t, err, "did not pass, so the merge posts no status and merges nothing")
	assert.Empty(t, rig.merger.landed(), "the red member is not merged")
}
