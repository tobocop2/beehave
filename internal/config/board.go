package config

import (
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

// rawBoard is a board block as written. At is the position of its call.
type rawBoard struct {
	At      syntax.Position
	Number  int
	Owner   string
	Started string
	Merged  string
}

// boardSpec is the schema of the board constructor.
func boardSpec() spec { return spec{"board", []string{"number", "started", "merged", "owner?"}} }

// newBoard is the constructor of the board block.
func newBoard(at syntax.Position, kwargs []starlark.Tuple) (*rawBoard, error) {
	raw := &rawBoard{At: at}
	err := boardSpec().unpack(at, kwargs, &raw.Number, &raw.Started, &raw.Merged, &raw.Owner)

	return raw, err
}

// boardOf checks the board block and returns it. A project with no block has no board.
func boardOf(raw rawProject) (model.Board, error) {
	block := raw.Board
	if block == nil {
		return model.Board{}, nil
	}

	if block.Number <= 0 {
		return model.Board{}, refuseAt(block.At, "`board.number` is %d. Write the number of the GitHub Project, a whole number above 0.", block.Number)
	}

	if err := requireText(block.At, "board.started", block.Started); err != nil {
		return model.Board{}, err
	}

	if err := requireText(block.At, "board.merged", block.Merged); err != nil {
		return model.Board{}, err
	}

	return model.Board{Owner: block.Owner, Number: block.Number, Started: block.Started, Merged: block.Merged}, nil
}
