package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

// withBoard makes the facts of the project hold a board.
func withBoard(owner string) func(string) model.Project {
	return func(project string) model.Project {
		return model.Project{Name: project, Board: model.Board{Owner: owner, Number: 3, Started: "In Progress", Merged: "Done"}}
	}
}

func TestExampleBoard_AProjectWithABoardMovesTheCardAtTheLaneStartAndAfterTheMergeAndReadsItBackBothTimes(t *testing.T) {
	t.Parallel()

	rig, file, _ := mergingExample(t, func(head string) *shipWorld { return &shipWorld{pull: shipPull(t, head, nil)} })
	rig.h.facts = withBoard("acme")

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{
		testProject + "|" + testItem + "|3|acme|0|In Progress",
		testProject + "|" + testItem + "|3|acme|0|Done",
	}, rig.h.forge.placed)
	assert.Equal(t, 2, rig.h.forge.cardReads, "each move reads the card back")
}

func TestExampleBoard_AProjectWithABoardThatLosesTheWriteStopsTheRunAtTheLaneStart(t *testing.T) {
	t.Parallel()

	rig, file, _ := mergingExample(t, func(head string) *shipWorld { return &shipWorld{pull: shipPull(t, head, nil)} })
	rig.h.facts = withBoard("")
	rig.h.forge.dropCards = true
	rig.h.forge.cardStatus = "Todo"

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, `shows "Todo"`)
	assert.Len(t, rig.h.agents.requests, 2, "only the plan and the implement agents ran: the run stops before the review loop")
}

func TestExampleBoard_ALandRunWithABoardMovesOneCardForEachMergedItem(t *testing.T) {
	t.Parallel()

	rig, file, _, _ := landingExample(t, reviewed("1 3"), "true")
	rig.h.facts = withBoard("acme")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{
		testProject + "|" + testItem + "|3|acme|1|Done",
		testProject + "|" + testItem + "|3|acme|3|Done",
	}, rig.h.forge.placed)
	assert.Equal(t, 2, rig.h.forge.cardReads, "each move reads the card back")
}

func TestExampleBoard_ALandRunMovesTheCardOfTheMergedItemAndNotTheCardOfARejectedItem(t *testing.T) {
	t.Parallel()

	rig, file, writer, _ := landingExample(t, reviewed("1 3"), "true")
	rig.h.facts = withBoard("acme")
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Lines: []string{"REFUSE: has the label hold"}}}

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|1|hold"}, writer.statuses, "item 1 is rejected and held")
	assert.Equal(t, []string{testProject + "|" + testItem + "|3|acme|3|Done"}, rig.h.forge.placed, "only the merged item 3 moves")
}

func TestExampleBoard_ALandRunWithNoBoardMovesNoCard(t *testing.T) {
	t.Parallel()

	rig, file, _, _ := landingExample(t, reviewed("1 3"), "true")
	rig.h.facts = ownedBy("ann")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
	assert.Empty(t, rig.h.forge.placed)
}

func TestExampleBoard_AProjectWithNoBoardMakesNoBoardCall(t *testing.T) {
	t.Parallel()

	rig, file, _ := mergingExample(t, func(head string) *shipWorld { return &shipWorld{pull: shipPull(t, head, nil)} })
	rig.h.facts = ownedBy("ann")

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
	assert.Empty(t, rig.h.forge.placed)
	assert.Zero(t, rig.h.forge.cardReads)
}
