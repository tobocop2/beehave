package github_test

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge/forgetest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

const (
	boardView  = `{"id":"PVT_1","number":3}`
	boardField = `{"fields":[{"id":"F0","name":"Title","type":"ProjectV2Field"},{"id":"F1","name":"Status","type":"ProjectV2SingleSelectField","options":[{"id":"o1","name":"Todo"},{"id":"o2","name":"In Review"}]}]}`
)

func boardForge(t *testing.T) (run *proctest.Scripted, place func(model.Card) error) {
	t.Helper()

	f, run := fixed(t, "")
	run.Plan(boardView, "project", "view")
	run.Plan(boardField, "project", "field-list")
	run.Plan(`{"id":"PVTI_9"}`, "project", "item-add")
	run.Plan("", "project", "item-edit")

	return run, func(card model.Card) error { return f.PlaceCard(context.Background(), card) }
}

func TestPlaceCard_AddsThePullRequestAndSetsTheOptionOfTheStatusField(t *testing.T) {
	t.Parallel()

	run, place := boardForge(t)

	require.NoError(t, place(model.Card{Board: 3, Number: 8, Pull: true, Status: "In Review"}))
	assert.Equal(t, [][]string{
		{"project", "view", "3", "--owner", "owner", "--format", "json"},
		{"project", "field-list", "3", "--owner", "owner", "--format", "json"},
		{"project", "item-add", "3", "--owner", "owner", "--url", "https://github.com/" + repo + "/pull/8", "--format", "json"},
		{"project", "item-edit", "--id", "PVTI_9", "--project-id", "PVT_1", "--field-id", "F1", "--single-select-option-id", "o2"},
	}, run.Calls)
}

// The repeat of a placement is safe because GitHub answers an add of a card that is on the board with the id of the existing card:
// "If you try to add an item that already exists, the existing item ID is returned instead."
// https://docs.github.com/en/issues/planning-and-tracking-with-projects/automating-your-project/using-the-api-to-manage-projects
// The edit then sets the Status field to the option that the call names, which is the same value again. No test touches a real board.
// This test pins the exact calls of both runs, so a change to a call that is not an add by address, or an edit that is not a set, turns it red.
func TestPlaceCard_ARepeatSendsTheSameAddByAddressAndTheSameSet(t *testing.T) {
	t.Parallel()

	run, place := boardForge(t)
	card := model.Card{Board: 3, Number: 8, Pull: true, Status: "In Review"}

	require.NoError(t, place(card))
	require.NoError(t, place(card))

	once := [][]string{
		{"project", "view", "3", "--owner", "owner", "--format", "json"},
		{"project", "field-list", "3", "--owner", "owner", "--format", "json"},
		{"project", "item-add", "3", "--owner", "owner", "--url", "https://github.com/" + repo + "/pull/8", "--format", "json"},
		{"project", "item-edit", "--id", "PVTI_9", "--project-id", "PVT_1", "--field-id", "F1", "--single-select-option-id", "o2"},
	}
	assert.Equal(t, append(slices.Clone(once), once...), run.Calls)
}

func TestPlaceCard_AnIssueGetsTheIssueAddressAndAnOwnerThatTheCallNamesIsUsed(t *testing.T) {
	t.Parallel()

	run, place := boardForge(t)

	require.NoError(t, place(model.Card{Board: 3, Owner: "acme", Number: 5, Status: "Todo"}))
	assert.Equal(t, []string{"project", "item-add", "3", "--owner", "acme", "--url", "https://github.com/" + repo + "/issues/5", "--format", "json"}, run.Calls[2])
}

func TestPlaceCard_AColumnThatTheBoardLacksIsRefusedBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	run, place := boardForge(t)

	err := place(model.Card{Board: 3, Number: 8, Pull: true, Status: "Done"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no column "Done"`)
	assert.Contains(t, err.Error(), "Todo, In Review")

	for _, call := range run.Calls {
		assert.NotContains(t, call, "item-add")
		assert.NotContains(t, call, "item-edit")
	}
}

func TestPlaceCard_ABoardWithNoStatusFieldIsRefused(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")
	run.Plan(boardView, "project", "view")
	run.Plan(`{"fields":[{"id":"F0","name":"Title","type":"ProjectV2Field"}]}`, "project", "field-list")

	err := f.PlaceCard(context.Background(), model.Card{Board: 3, Number: 8, Pull: true, Status: "Todo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Status field")
}

func TestPlaceCard_AFailedCallRefusesAndSaysWhichStepFailed(t *testing.T) {
	t.Parallel()

	for _, word := range []string{"view", "field-list", "item-add", "item-edit"} {
		f, run := fixed(t, "")
		run.Plan(boardView, "project", "view")
		run.Plan(boardField, "project", "field-list")
		run.Plan(`{"id":"PVTI_9"}`, "project", "item-add")
		run.Plan("", "project", "item-edit")
		run.PlanFail("HTTP 403", "project", word)

		err := f.PlaceCard(context.Background(), model.Card{Board: 3, Number: 8, Pull: true, Status: "Todo"})
		require.Error(t, err, word)
		assert.Contains(t, err.Error(), word, word)
	}
}

func TestPlaceCard_AnAnswerOfTheWrongShapeIsRefused(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")
	run.Plan(`{"id":""}`, "project", "view")

	require.Error(t, f.PlaceCard(context.Background(), model.Card{Board: 3, Number: 8, Pull: true, Status: "Todo"}))

	g, run := fixed(t, "")
	run.Plan(boardView, "project", "view")
	run.Plan(boardField, "project", "field-list")
	run.Plan(`{"id":""}`, "project", "item-add")

	err := g.PlaceCard(context.Background(), model.Card{Board: 3, Number: 8, Pull: true, Status: "Todo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no id for the card")

	for _, call := range run.Calls {
		assert.NotContains(t, call, "item-edit")
	}
}

const boardItems = `{"totalCount":3,"items":[
{"id":"a","status":"Todo","content":{"type":"Issue","number":8,"repository":"owner/name"}},
{"id":"b","status":"In Review","content":{"type":"PullRequest","number":8,"repository":"owner/name"}},
{"id":"c","status":"Done","content":{"type":"PullRequest","number":8,"repository":"other/repo"}}]}`

func TestCardStatus_ReadsTheStatusOfTheCardOfThisRepositoryAndKind(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, boardItems)

	status, err := f.CardStatus(context.Background(), model.Card{Board: 3, Number: 8, Pull: true})
	require.NoError(t, err)
	assert.Equal(t, "In Review", status)
	assert.Equal(t, [][]string{{"project", "item-list", "3", "--owner", "owner", "--format", "json", "--limit", "1000"}}, run.Calls)

	status, err = f.CardStatus(context.Background(), model.Card{Board: 3, Number: 8})
	require.NoError(t, err)
	assert.Equal(t, "Todo", status)
}

func TestBoarder_ConformsToTheFailedCommandRules(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "HTTP 502")

	forgetest.CheckBoarderFails(t, down)
}

func TestCardStatus_ACardThatIsNotOnTheBoardOrAListThatIsCutShortIsAnError(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, `{"totalCount":0,"items":[]}`)
	_, err := f.CardStatus(context.Background(), model.Card{Board: 3, Number: 8, Pull: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not on board 3")

	cut, _ := fixed(t, `{"totalCount":2000,"items":[]}`)
	_, err = cut.CardStatus(context.Background(), model.Card{Board: 3, Number: 8, Pull: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cut short")

	down, _ := failing(t, "HTTP 502")
	_, err = down.CardStatus(context.Background(), model.Card{Board: 3, Number: 8, Pull: true})
	require.Error(t, err)

	odd, _ := fixed(t, `null`)
	_, err = odd.CardStatus(context.Background(), model.Card{Board: 3, Number: 8, Pull: true})
	require.Error(t, err)
}
