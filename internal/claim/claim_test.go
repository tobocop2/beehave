package claim_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/claim"
	"github.com/tobocop2/obee/internal/model"
)

func ctx() context.Context {
	return context.Background()
}

func TestItemOf(t *testing.T) {
	t.Parallel()

	got, err := claim.ItemOf("12")
	require.NoError(t, err)
	assert.Equal(t, "12", got)

	_, err = claim.ItemOf("x y")
	require.EqualError(t, err, "claim: 'x y' is not an item id")
	assert.Equal(t, claim.CodeRefused, newHarness(t).code(err))
}

func TestOwnerOf(t *testing.T) {
	t.Parallel()

	for _, token := range []string{"A", "R2", "run_1", "a9"} {
		got, err := claim.OwnerOf(token)
		require.NoError(t, err)
		assert.Equal(t, "session-"+token, got)
	}

	for _, token := range []string{"", "a-b", "a b", "a.b", "é"} {
		_, err := claim.OwnerOf(token)
		assert.ErrorContains(t, err, "is not an owner token", token)
	}
}

func TestTake_ClaimsAFreeItemWithoutAReclaim(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))

	assert.Equal(t, "TAKEN #5 by session-A\n", h.said())
	assert.Equal(t, sessionA, h.rows.rows["5"].Holder)
	assert.Equal(t, 1, h.rows.calls["TryHold"])
	assert.Zero(t, h.rows.calls["Reclaim"])
}

func TestTake_OfAnItemYouHoldChangesNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, 60)
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))

	assert.Equal(t, "#5 already held by you\n", h.said())
	assert.Zero(t, h.rows.calls["TryHold"])
}

func TestTake_RefusesALiveHolderWithCodeThreeAfterOneReclaim(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, 60)

	err := h.claims.Take(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 is held by session-B. Do not touch it.")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Equal(t, 1, h.rows.calls["Reclaim"], "the reclaim frees a row only when its lease ended")
	assert.Equal(t, sessionB, h.rows.rows["5"].Holder)
}

func TestTake_ReclaimsAnEndedLeaseAndTakesTheItem(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, -1)

	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Equal(t, "reclaiming #5 from session-B: its lease ended\nTAKEN #5 by session-A\n", h.said())
	assert.Equal(t, sessionA, h.rows.rows["5"].Holder)
}

func TestTake_RefusesARowWithNoLease(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.holdBare("5", sessionB)

	err := h.claims.Take(ctx(), "A", "5")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Equal(t, sessionB, h.rows.rows["5"].Holder)
}

func TestTake_AClaimThatLostTheRaceNamesTheWinner(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.onCall = func(method string, _ int) {
		if method == "TryHold" {
			h.hold("5", sessionB, 60)
		}
	}

	err := h.claims.Take(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 is held by session-B. Do not touch it.")
	assert.Equal(t, claim.CodeHeld, h.code(err))
}

func TestTake_AClaimThatDidNotLandIsTriedAgain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.deaf = 1

	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Equal(t, 2, h.rows.calls["TryHold"])
}

func TestTake_AClaimThatNeverLandsStopsAfterFourAttempts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.deaf = 99

	err := h.claims.Take(ctx(), "A", "5")
	require.ErrorContains(t, err, "could not take #5 after 4 attempts; check `obee claim who 5`")
	assert.Equal(t, claim.CodeRefused, h.code(err))
	assert.Equal(t, 4, h.rows.calls["TryHold"])
}

func TestEnsureFree(t *testing.T) {
	t.Parallel()

	const refused = "REFUSED: #5 is held by session-B. Do not touch it."

	tests := map[string]struct {
		hold func(h *harness)
		want string
	}{
		"free":          {func(*harness) {}, ""},
		"mine":          {func(h *harness) { h.hold("5", sessionA, 60) }, ""},
		"live_other":    {func(h *harness) { h.hold("5", sessionB, 60) }, refused},
		"ended_other":   {func(h *harness) { h.hold("5", sessionB, -1) }, ""},
		"no_lease_held": {func(h *harness) { h.holdBare("5", sessionB) }, refused},
	}

	for name, tt := range tests {
		h := newHarness(t)
		tt.hold(h)

		err := h.claims.EnsureFree(ctx(), "A", "5")
		if tt.want == "" {
			require.NoError(t, err, name)
		} else {
			require.EqualError(t, err, tt.want, name)
			assert.Equal(t, claim.CodeHeld, h.code(err), name)
		}

		assert.Zero(t, h.rows.calls["TryHold"]+h.rows.calls["Free"]+h.rows.calls["Reclaim"], name)
	}
}

func TestBeat(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, 10)
	problem, err := h.claims.Beat(ctx(), "A")
	require.NoError(t, err)
	assert.Empty(t, problem)
	assert.Equal(t, time.Unix(nowUnix+leaseSeconds, 0), h.rows.rows["5"].LeaseEnds)

	problem, err = h.claims.Beat(ctx(), "B")
	require.NoError(t, err)
	assert.Equal(t, "heartbeat for session-B failed: the holder holds no claim", problem)

	h.rows.fail["Beat"] = errBoom
	problem, err = h.claims.Beat(ctx(), "A")
	require.ErrorIs(t, err, errBoom, "a heartbeat that cannot run is not a lost claim")
	require.EqualError(t, err, "heartbeat for session-A failed: boom")
	assert.Empty(t, problem)

	_, err = h.claims.Beat(ctx(), "a b")
	assert.ErrorContains(t, err, "is not an owner token")
}

func TestWho(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		hold func(h *harness)
		want string
	}{
		"live":     {func(h *harness) { h.hold("5", sessionB, 10) }, "#5 held by session-B"},
		"ended":    {func(h *harness) { h.hold("5", sessionB, -3600) }, "#5 held by session-B (LEASE ENDED 1h00m ago; `take` reclaims it)"},
		"no_lease": {func(h *harness) { h.holdBare("5", sessionB) }, "#5 held by session-B (no lease: release it by hand)"},
	}

	for name, tt := range tests {
		h := newHarness(t)
		tt.hold(h)

		got, err := h.claims.Who(ctx(), "5")
		require.NoError(t, err, name)
		assert.Equal(t, tt.want, got, name)
	}
}

func TestWho_SaysUnownedOrManyHolders(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	got, err := h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 unowned", got)

	h.hold("5", sessionB, 10)
	h.rows.extra = []model.ClaimRow{{Item: "5", Holder: sessionC}, {Item: "6", Holder: sessionA}, {Item: "5", Holder: sessionC}}
	got, err = h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 held by session-B, session-C (MORE THAN ONE HOLDER: resolve by hand)", got)
}

func TestRelease(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, 60)
	require.NoError(t, h.claims.Release(ctx(), "A", "5"))
	assert.Equal(t, "released #5\n", h.said())
	assert.Empty(t, h.rows.rows)

	h = newHarness(t)
	require.NoError(t, h.claims.Release(ctx(), "A", "5"))
	assert.Equal(t, "#5 was not locked\n", h.said())
	assert.Zero(t, h.rows.calls["Free"])

	h.hold("5", sessionB, 60)
	err := h.claims.Release(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 is held by session-B, not session-A")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Contains(t, h.rows.rows, "5")
}

func TestRelease_OfARowThatChangedHandsAfterTheReadIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, 60)
	h.rows.fail["Free"] = model.ErrRowMoved

	err := h.claims.Release(ctx(), "A", "5")

	require.EqualError(t, err, "REFUSED: #5 was claimed by another owner in the same window. Do not touch it.")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Empty(t, h.said())
}

func TestEveryReadAndWriteThatFailsIsARefusalThatKeepsItsCause(t *testing.T) {
	t.Parallel()

	steps := map[string]func(h *harness) error{
		"take":        func(h *harness) error { return h.claims.Take(ctx(), "A", "5") },
		"release":     func(h *harness) error { return h.claims.Release(ctx(), "A", "5") },
		"ensure_free": func(h *harness) error { return h.claims.EnsureFree(ctx(), "A", "5") },
		"who":         func(h *harness) error { _, err := h.claims.Who(ctx(), "5"); return err },
		"reclaim":     func(h *harness) error { return h.claims.Take(ctx(), "A", "5") },
	}
	tests := []struct{ step, method string }{
		{"take", "HeldRows"},
		{"take", "TryHold"},
		{"release", "HeldRows"},
		{"release", "Free"},
		{"ensure_free", "HeldRows"},
		{"who", "HeldRows"},
		{"reclaim", "Reclaim"},
	}

	for _, tt := range tests {
		h := newHarness(t)

		switch tt.step {
		case "reclaim":
			h.hold("5", sessionB, -1)
		case "release":
			h.hold("5", sessionA, 60)
		}

		h.rows.fail[tt.method] = errBoom
		err := steps[tt.step](h)

		require.ErrorIs(t, err, errBoom, tt.step+" "+tt.method)
		assert.Equal(t, claim.CodeRefused, h.code(err), tt.step+" "+tt.method)
	}
}

func TestATakeWhoseSecondReadFailsIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.onCall = func(method string, call int) {
		if method == "HeldRows" && call == 2 {
			h.rows.fail["HeldRows"] = errBoom
		}
	}

	require.ErrorIs(t, h.claims.Take(ctx(), "A", "5"), errBoom)
}

func TestARefusalFromTheTrackerKeepsItsCode(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.fail["HeldRows"] = &claim.Error{Code: claim.CodeHeld, Message: "held"}

	assert.Equal(t, claim.CodeHeld, h.code(h.claims.Take(ctx(), "A", "5")))
}

func TestBadNamesAreRefusedBeforeAnyRowIsRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	for _, err := range []error{
		h.claims.Take(ctx(), "a b", "5"), h.claims.Take(ctx(), "A", "x y"), h.claims.Release(ctx(), "", "5"),
		h.claims.Release(ctx(), "A", "x y"), h.claims.EnsureFree(ctx(), "a b", "5"), h.claims.EnsureFree(ctx(), "A", "x y"),
	} {
		assert.Equal(t, claim.CodeRefused, h.code(err))
	}

	_, err := h.claims.Who(ctx(), "x y")
	assert.Equal(t, claim.CodeRefused, h.code(err))
	assert.Empty(t, h.rows.calls)
}

func TestNew_RefusesAMissingDependency(t *testing.T) {
	t.Parallel()

	good := claim.Options{Rows: newFakeRows(), Clock: time.Now, Out: &bytes.Buffer{}}
	tests := map[string]struct {
		change func(*claim.Options)
		want   string
	}{
		"no_rows":  {func(o *claim.Options) { o.Rows = nil }, "the claim rows is required"},
		"no_clock": {func(o *claim.Options) { o.Clock = nil }, "the clock is required"},
		"no_out":   {func(o *claim.Options) { o.Out = nil }, "the output writer is required"},
	}

	for name, tt := range tests {
		options := good
		tt.change(&options)

		claims, err := claim.New(options)
		require.ErrorContains(t, err, tt.want, name)
		assert.Nil(t, claims, name)
		assert.Equal(t, claim.CodeRefused, newHarness(t).code(err), name)
	}

	claims, err := claim.New(good)
	require.NoError(t, err)
	assert.NotNil(t, claims)
}

func TestError_UnwrapsItsCause(t *testing.T) {
	t.Parallel()

	failure := &claim.Error{Code: claim.CodeRefused, Message: "m", Err: errBoom}
	assert.ErrorIs(t, failure, errBoom)
}
