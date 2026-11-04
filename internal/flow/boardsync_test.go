package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoardSync_PlacesTheCardAndReadsItBackBeforeItReturnsTheStatus(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `card = board_sync(board = 3, owner = "acme", status = "In Review", issue = 5, name = "card")
command(run = [str(card.number), card.status])
`)
	require.NoError(t, err)

	assert.Equal(t, []string{testProject + "|" + testItem + "|3|acme|5|In Review"}, f.placed)
	assert.Equal(t, 1, f.cardReads)
	assert.Equal(t, []string{"7 In Review"}, h.commands.ran())
}

func TestBoardSync_ABoardThatDropsTheWriteFailsTheStepAtTheReadBack(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)
	f.dropCards = true
	f.cardStatus = "Todo"

	_, err := walkBody(t, h, `board_sync(board = 3, status = "Done")
command(run = ["after"])
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `shows "Todo"`)
	assert.Contains(t, err.Error(), `"Done"`)
	assert.Empty(t, h.commands.ran(), "the steps after the failed read-back never run")
}

func TestBoardSync_ACardThatIsOnNoColumnFailsTheStepAndSaysSo(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)
	f.dropCards = true

	_, err := walkBody(t, h, `board_sync(board = 3, status = "Done")
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no status")
}

func TestBoardSync_TheSameCallTwiceLeavesTheCardOnTheColumn(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `a = board_sync(board = 3, status = "Done", name = "one")
b = board_sync(board = 3, status = "Done", name = "two")
command(run = [a.status, b.status])
`)
	require.NoError(t, err)
	assert.Equal(t, "Done", f.cardStatus)
	assert.Equal(t, []string{"Done Done"}, h.commands.ran())
}

func TestBoardSync_AFailedWriteOrReadFailsTheStep(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)
	f.writeErr = errFake

	_, err := walkBody(t, h, `board_sync(board = 3, status = "Done")
`)
	require.ErrorIs(t, err, errFake)
	assert.Zero(t, f.cardReads, "no read-back follows a failed write")

	h, f = prHarness(t)
	f.readErr = errFake

	_, err = walkBody(t, h, `board_sync(board = 3, status = "Done")
`)
	require.ErrorIs(t, err, errFake)
}

func TestBoardSync_BadArgumentsFailAtTheCall(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"no board":          `board_sync(status = "Done")`,
		"a zero board":      `board_sync(board = 0, status = "Done")`,
		"a negative board":  `board_sync(board = -1, status = "Done")`,
		"no status":         `board_sync(board = 3)`,
		"an empty status":   `board_sync(board = 3, status = "")`,
		"a negative issue":  `board_sync(board = 3, status = "Done", issue = -1)`,
		"a text board":      `board_sync(board = "3", status = "Done")`,
		"an unknown option": `board_sync(board = 3, status = "Done", column = "x")`,
	} {
		h, f := prHarness(t)

		_, err := walkBody(t, h, call+"\n")
		require.Error(t, err, name)
		assert.Empty(t, f.placed, name)
	}
}
