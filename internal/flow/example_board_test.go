package flow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
)

// boardVars is the key `board` of the vars of a project. An empty owner leaves the key out, as a project file does.
func boardVars(owner string) map[string]any {
	board := map[string]any{"number": json.Number("3"), "started": "In Progress", "merged": "Done"}
	if owner != "" {
		board["owner"] = owner
	}

	return board
}

// setBoard makes the vars of the project of the rig hold a board.
func setBoard(rig *agentRig, owner string) {
	rig.project.Vars["board"] = boardVars(owner)
}

// roundBoard gives the project of a round rig the board of the tests.
func roundBoard(rig *roundRig, owner string) {
	rig.project.Vars["board"] = boardVars(owner)
}

// boardRuns returns the runs of `remedy board` among the commands that the example ran, words joined by a space.
func boardRuns(ran []string) []string {
	var runs []string

	for _, run := range ran {
		if strings.HasPrefix(run, "remedy board ") {
			runs = append(runs, run)
		}
	}

	return runs
}

func TestExampleBoard_AProjectWithABoardRunsTheBoardCommandForThePullRequestAtTheLaneStartAndTheChildMovesNoMergedCard(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	setBoard(rig, "acme")

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"remedy board --board 3 --status In Progress --owner acme --pr 7"}, boardRuns(rig.h.commands.ran()), "the merged column is the round's: the child never merges")
}

func TestExampleBoard_AProjectWithABoardThatHasNoOwnerLeavesTheOwnFlagOut(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	setBoard(rig, "")

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"remedy board --board 3 --status In Progress --pr 7"}, boardRuns(rig.h.commands.ran()))
}

func TestExampleBoard_ABoardCommandThatExitsWithTwoStopsTheRunAtTheLaneStart(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	setBoard(rig, "")

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 1 && request.Run[0] == "remedy" && request.Run[1] == "board" {
			return CommandOutput{RC: 2, Stderr: `remedy board: the board 3 shows "Todo" for the card after the write; wanted "In Progress"`}, nil
		}

		if previous == nil {
			return CommandOutput{}, nil
		}

		return previous(request)
	}

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped, "a card that shows another column ends the item")
	assert.Equal(t, "board_card:board-started#1", result.Stopped.Step)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Len(t, rig.h.agents.requests, 3, "only the plan, the implement and the sweep agents ran: the run stops before the review loop")
}

func TestExampleBoard_ABoardInTheVarsThatLacksAColumnStopsTheRunAtTheLaneStartAndNamesTheKey(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	board := boardVars("")
	delete(board, "merged")
	rig.project.Vars["board"] = board

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, "vars.board has no merged")
	assert.Empty(t, boardRuns(rig.h.commands.ran()))
}

func TestExampleBoard_ALandRunWithABoardRunsTheBoardCommandForEachMergedIssue(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	roundBoard(rig, "acme")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"remedy board --board 3 --status Done --owner acme --issue 1",
		"remedy board --board 3 --status Done --owner acme --issue 3",
	}, doneMoves(rig))
}

// doneMoves returns the runs of the board command that move a card to the merged column. Each child also moves a card to the started column; the land run makes the others.
func doneMoves(rig *roundRig) []string {
	var moves []string

	for _, run := range boardRuns(rig.h.commands.ran()) {
		if strings.Contains(run, "--status Done") {
			moves = append(moves, run)
		}
	}

	return moves
}

func TestExampleBoard_ALandRunMovesTheCardOfTheMergedItemAndNotTheCardOfARejectedItem(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	roundBoard(rig, "acme")
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Lines: []string{"REFUSE: has the label hold"}, Refusal: []string{"REFUSE: has the label hold"}}}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer), "item 1 is rejected and held")
	assert.Equal(t, []string{"remedy board --board 3 --status Done --owner acme --issue 3"}, doneMoves(rig), "only the merged item 3 moves")
}

func TestExampleBoard_ALandRunWithNoBoardMovesNoCard(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed(), "the members end in turn, so they land in list order")
	assert.Empty(t, boardRuns(rig.h.commands.ran()))
}

func TestExampleBoard_AProjectWithNoBoardMakesNoBoardCall(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved)
	assert.Empty(t, boardRuns(rig.h.commands.ran()))
}
