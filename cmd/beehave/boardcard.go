package main

import (
	"context"
	"fmt"

	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

// PlaceCard puts the card of the pull request of the item, or of the issue that the request names, on the project board and sets its status column.
// It returns the number of the pull request or issue. A project with no forge, a forge that keeps no boards and an item with no pull request are errors.
func (p pulls) PlaceCard(ctx context.Context, project, item string, req flow.CardRequest) (int, error) {
	boarder, card, err := p.cardOf(ctx, project, item, req)
	if err != nil {
		return 0, err
	}

	return card.Number, boarder.PlaceCard(ctx, card)
}

// ReadCard reads the status column of the same card from the board.
func (p pulls) ReadCard(ctx context.Context, project, item string, req flow.CardRequest) (string, error) {
	boarder, card, err := p.cardOf(ctx, project, item, req)
	if err != nil {
		return "", err
	}

	return boarder.CardStatus(ctx, card)
}

// cardOf returns the board side of the forge of the project and the card of the request. An issue number in the request is the card; without it the card is
// the pull request on the branch of the item.
func (p pulls) cardOf(ctx context.Context, project, item string, req flow.CardRequest) (forge.Boarder, model.Card, error) { //nolint:ireturn // the optional board side of the forge is an interface by design
	built, err := p.forgeOf(project)
	if err != nil {
		return nil, model.Card{}, err
	}

	boarder, ok := built.Forge.(forge.Boarder)
	if !ok {
		return nil, model.Card{}, fmt.Errorf("%w: a project board", errNoOptionalRead)
	}

	card := model.Card{Board: req.Board, Owner: req.Owner, Number: req.Issue, Status: req.Status}
	if req.Issue != 0 {
		return boarder, card, nil
	}

	_, card.Number, err = p.numberOf(ctx, built, item)
	card.Pull = true

	return boarder, card, err
}
