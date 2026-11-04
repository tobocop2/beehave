package claim_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/claim"
)

func TestOthers_ListsEveryOtherOwnerWithItsItemsInOrder(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("9", sessionB, 60)
	h.hold("7", sessionB, 60)
	h.hold("3", sessionC, 60)
	h.hold("5", sessionA, 60)

	others, err := h.claims.Others(ctx(), "A")

	require.NoError(t, err)
	assert.Equal(t, []claim.Holding{{Holder: sessionB, Items: []string{"7", "9"}}, {Holder: sessionC, Items: []string{"3"}}}, others)
}

func TestOthers_LeavesOutAnOwnerWhoseLeaseEnded(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("9", sessionB, -1)
	h.holdBare("3", sessionC)

	others, err := h.claims.Others(ctx(), "A")

	require.NoError(t, err)
	assert.Equal(t, []claim.Holding{{Holder: sessionC, Items: []string{"3"}}}, others, "a row with no lease never ends, so its owner still runs")
}

func TestOthers_OfAStoreWithNoOtherOwnerIsEmpty(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, 60)

	others, err := h.claims.Others(ctx(), "A")

	require.NoError(t, err)
	assert.Empty(t, others)
}

func TestOthers_FailsClosedWhenTheStoreCannotBeRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.fail["HeldRows"] = errBoom

	others, err := h.claims.Others(ctx(), "A")

	require.ErrorIs(t, err, errBoom)
	assert.Equal(t, claim.CodeRefused, h.code(err))
	assert.Nil(t, others, "an unreadable store gives no list that could pass for an empty one")
}

func TestOthers_RefusesABadOwnerToken(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.claims.Others(ctx(), "a-b")

	require.ErrorContains(t, err, "is not an owner token")
	assert.Zero(t, h.rows.calls["HeldRows"])
}
