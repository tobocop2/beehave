package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
)

// boardForge finds the pull request of a branch and keeps a board in memory. The methods it does not define panic.
type boardCardForge struct {
	openForge

	placed  []model.Card
	status  map[int]string // number -> column that the board holds
	dropped bool           // the board loses the write
}

func (f *boardCardForge) PlaceCard(_ context.Context, card model.Card) error {
	f.placed = append(f.placed, card)

	if !f.dropped {
		f.status[card.Number] = card.Status
	}

	return nil
}

func (f *boardCardForge) CardStatus(_ context.Context, card model.Card) (string, error) {
	return f.status[card.Number], nil
}

func newBoardCardForge() *boardCardForge {
	return &boardCardForge{openForge: *newOpenForge(), status: map[int]string{}}
}

func TestPullsPlaceCard_CardsThePullRequestOfTheItemAndReadsItBack(t *testing.T) {
	t.Parallel()

	f := newBoardCardForge()
	f.open["bb-5"] = 12

	number, err := openOver(f).PlaceCard(t.Context(), "p", "bb-5", flow.CardRequest{Board: 3, Owner: "acme", Status: "Done"})
	require.NoError(t, err)
	assert.Equal(t, 12, number)
	assert.Equal(t, []model.Card{{Board: 3, Owner: "acme", Number: 12, Pull: true, Status: "Done"}}, f.placed)

	shown, err := openOver(f).ReadCard(t.Context(), "p", "bb-5", flow.CardRequest{Board: 3, Owner: "acme", Status: "Done"})
	require.NoError(t, err)
	assert.Equal(t, "Done", shown)
}

func TestPullsPlaceCard_AnIssueNumberCardsTheIssueAndNeedsNoPullRequest(t *testing.T) {
	t.Parallel()

	f := newBoardCardForge()

	number, err := openOver(f).PlaceCard(t.Context(), "p", "bb-5", flow.CardRequest{Board: 3, Issue: 9, Status: "Todo"})
	require.NoError(t, err)
	assert.Equal(t, 9, number)
	assert.Equal(t, []model.Card{{Board: 3, Number: 9, Status: "Todo"}}, f.placed)
}

func TestPullsPlaceCard_AnItemWithNoPullRequestIsAnError(t *testing.T) {
	t.Parallel()

	_, err := openOver(newBoardCardForge()).PlaceCard(t.Context(), "p", "bb-5", flow.CardRequest{Board: 3, Status: "Todo"})
	require.Error(t, err)

	_, err = openOver(newBoardCardForge()).ReadCard(t.Context(), "p", "bb-5", flow.CardRequest{Board: 3, Status: "Todo"})
	require.Error(t, err)
}

func TestPullsPlaceCard_AProjectWithNoBoardForgeOrNoConfigIsRefused(t *testing.T) {
	t.Parallel()

	_, err := openOver(plainForge{}).PlaceCard(t.Context(), "p", "bb-5", flow.CardRequest{Board: 3, Issue: 9, Status: "Todo"})
	require.ErrorIs(t, err, errNoOptionalRead)

	_, err = openOver(plainForge{}).ReadCard(t.Context(), "p", "bb-5", flow.CardRequest{Board: 3, Issue: 9, Status: "Todo"})
	require.ErrorIs(t, err, errNoOptionalRead)

	_, err = openOver(newBoardCardForge()).PlaceCard(t.Context(), "nope", "bb-5", flow.CardRequest{Board: 3, Issue: 9, Status: "Todo"})
	require.ErrorIs(t, err, errUnknownProject)
}
