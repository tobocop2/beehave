package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestLoadFolder_TheBoardBlockGivesTheOwnerTheNumberAndTheTwoColumns(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, "\n"+call(localProject+`, board = board(number = 3, owner = "acme", started = "In Progress", merged = "Done")`))

	require.Empty(t, problems)
	require.Equal(t, model.Board{Owner: "acme", Number: 3, Started: "In Progress", Merged: "Done"}, project.Board)
}

func TestLoadFolder_AProjectWithNoBoardBlockHasNoBoard(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject))

	require.Empty(t, problems)
	require.Equal(t, model.Board{}, project.Board)
}

func TestLoadFolder_ABoardWithoutAnOwnerLeavesTheOwnerToTheRepository(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject+`, board = board(number = 3, started = "a", merged = "b")`))

	require.Empty(t, problems)
	require.Empty(t, project.Board.Owner)
}

func TestLoadFolder_ABadBoardIsRefusedWithTheFileAndTheLine(t *testing.T) {
	t.Parallel()

	for name, block := range map[string]string{
		"number zero":       `board(number = 0, started = "a", merged = "b")`,
		"a negative number": `board(number = -2, started = "a", merged = "b")`,
		"a text number":     `board(number = "3", started = "a", merged = "b")`,
		"no number":         `board(started = "a", merged = "b")`,
		"no started column": `board(number = 3, merged = "b")`,
		"a blank started":   `board(number = 3, started = " ", merged = "b")`,
		"a blank merged":    `board(number = 3, started = "a", merged = "")`,
		"an unknown key":    `board(number = 3, started = "a", merged = "b", color = "red")`,
	} {
		_, problems := loadProjectText(t, "\n"+call(localProject+", board = "+block))

		require.NotEmpty(t, problems, name)
		require.Contains(t, problems[0].Error(), "p.star:2:", name)
	}
}

func TestLoadFolder_ASecondBoardBlockIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, "_a = board(number = 1, started = \"a\", merged = \"b\")\n"+call(localProject+`, board = board(number = 3, started = "a", merged = "b")`))

	require.NotEmpty(t, problems)
}
