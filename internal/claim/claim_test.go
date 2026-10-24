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
	require.EqualError(t, err, "claim: 'x y' is neither a forge number nor a beads id")
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

func TestAge(t *testing.T) {
	t.Parallel()

	tests := map[float64]string{0: "0h00m", 59: "0h00m", 60: "0h01m", 3599: "0h59m", 3600: "1h00m", 7200: "2h00m", 7260: "2h01m"}
	for seconds, want := range tests {
		assert.Equal(t, want, claim.Age(seconds))
	}
}

func TestTake_BeatsHoldsAndStoresTheLease(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))

	assert.Equal(t, "TAKEN #5 by session-A\n", h.said())
	assert.Equal(t, model.ClaimRow{Item: "5", Holder: sessionA, Lease: lease, Nonce: "take-1"}, h.rows.rows["5"])
	assert.Equal(t, time.Unix(nowUnix, 0), h.rows.seen[sessionA])
	assert.Equal(t, []time.Duration{500 * time.Millisecond}, h.slept)
	assert.Equal(t, 1, h.rows.calls["StoreLease"])
}

func TestTake_OfAnItemYouHoldChangesNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, lease, 0)
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))

	assert.Equal(t, "#5 already held by you\n", h.said())
	assert.Zero(t, h.rows.calls["TryHold"])
}

func TestTake_RefusesALiveHolderWithCodeThree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, lease, 60)

	err := h.claims.Take(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 is held by session-B. Do not touch it.")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Zero(t, h.rows.calls["TryHold"])
}

func TestEnsureFree(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		holder string
		ago    int64
		want   string
	}{
		"free":            {},
		"mine":            {holder: sessionA, ago: 0},
		"live_other":      {holder: sessionB, ago: 5, want: "REFUSED: #5 is held by session-B. Do not touch it."},
		"expired_other":   {holder: sessionB, ago: lease + 1},
		"never_beat_held": {holder: sessionB, ago: -1, want: "REFUSED: #5 is held by session-B. Do not touch it."},
	}

	for name, tt := range tests {
		h := newHarness(t)
		if tt.holder != "" {
			h.hold("5", tt.holder, model.LeaseNone, tt.ago)
		}

		err := h.claims.EnsureFree(ctx(), "A", "5")
		if tt.want == "" {
			require.NoError(t, err, name)
		} else {
			require.EqualError(t, err, tt.want, name)
		}

		assert.Zero(t, h.rows.calls["TryHold"]+h.rows.calls["Free"], name)
	}
}

func TestExpiry(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		stored  int64
		ago     int64
		expired bool
	}{
		"on_the_lease":            {stored: model.LeaseNone, ago: lease, expired: false},
		"one_second_past":         {stored: model.LeaseNone, ago: lease + 1, expired: true},
		"stored_lease_is_used":    {stored: 100, ago: 101, expired: true},
		"stored_lease_not_past":   {stored: 100, ago: 100, expired: false},
		"stored_longer_than_ours": {stored: 100_000, ago: lease + 1, expired: false},
		"unreadable_never":        {stored: model.LeaseUnreadable, ago: 1_000_000, expired: false},
		"no_beat_never":           {stored: model.LeaseNone, ago: -1, expired: false},
	}

	for name, tt := range tests {
		h := newHarness(t)
		h.hold("5", sessionB, tt.stored, tt.ago)

		err := h.claims.Take(ctx(), "A", "5")
		if tt.expired {
			require.NoError(t, err, name)
			assert.Equal(t, sessionA, h.rows.rows["5"].Holder, name)
			assert.Contains(t, h.said(), "reclaiming #5 from session-B", name)
		} else {
			assert.Equal(t, claim.CodeHeld, h.code(err), name)
		}
	}
}

func TestTake_TheReclaimMessageNamesTheLeaseTheHolderStored(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, 100, 7260)
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Contains(t, h.said(), "reclaiming #5 from session-B: last seen 2h01m ago, lease 0h01m\n")

	h = newHarness(t)
	h.hold("5", sessionB, model.LeaseNone, lease+60)
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Contains(t, h.said(), "lease 2h00m\n")
}

func TestTake_AnExpiredRowIsFreedOnlyWhenTheHolderStillHoldsIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, model.LeaseNone, lease+1)
	h.rows.onCall = func(method string, _ int) {
		if method == "Row" && h.rows.calls["Row"] == 1 {
			h.rows.rows["5"] = model.ClaimRow{Item: "5", Holder: sessionC}
		}
	}

	err := h.claims.Take(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 was claimed by session-C in the same window.")
	assert.Zero(t, h.rows.calls["Free"])
	assert.NotContains(t, h.said(), "reclaiming")
}

func TestTake_ARowThatIsGoneBeforeTheReclaimIsClaimedWithoutAFree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, model.LeaseNone, lease+1)
	h.rows.onCall = func(method string, _ int) {
		if method == "Row" && h.rows.calls["Row"] == 1 {
			delete(h.rows.rows, "5")
		}
	}

	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Zero(t, h.rows.calls["Free"])
}

func TestTake_AClaimThatLostTheRaceNamesTheWinner(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.onCall = func(method string, _ int) {
		if method == "TryHold" {
			h.rows.rows["5"] = model.ClaimRow{Item: "5", Holder: sessionB}
		}
	}

	err := h.claims.Take(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 was claimed by session-B in the same window.")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Zero(t, h.rows.calls["StoreLease"])
}

func TestTake_ARowOfTheSameHolderWithoutATokenIsNotTheTake(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.onCall = func(method string, _ int) {
		if method == "TryHold" {
			h.rows.rows["5"] = model.ClaimRow{Item: "5", Holder: sessionA}
		}
	}

	err := h.claims.Take(ctx(), "A", "5")
	require.Error(t, err, "a row with no token was not written by this take")
	assert.Equal(t, claim.CodeRefused, h.code(err))
	assert.Zero(t, h.rows.calls["StoreLease"])
}

func TestTake_ARowTakenOverDuringTheSettlePauseIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.onCall = func(method string, call int) {
		if method == "Row" && call == 2 {
			h.rows.rows["5"] = model.ClaimRow{Item: "5", Holder: sessionB}
		}
	}

	err := h.claims.Take(ctx(), "A", "5")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Len(t, h.slept, 1)
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
	require.ErrorContains(t, err, "could not settle ownership of #5 after 4 attempts; check `obee claim who 5`")
	assert.Equal(t, claim.CodeRefused, h.code(err))
	assert.Equal(t, 4, h.rows.calls["TryHold"])
}

func TestTake_AFailedHeartbeatWarnsAndTheTakeGoesOn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.fail["Beat"] = errBoom

	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Equal(t, "WARNING: heartbeat for session-A failed: boom. Other runs will read this owner's claims as expired after 2h00m.\n", h.warn.String())
	assert.Equal(t, "TAKEN #5 by session-A\n", h.said())
}

func TestBeat(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	problem, err := h.claims.Beat(ctx(), "A")
	require.NoError(t, err)
	assert.Empty(t, problem)

	h.rows.fail["Beat"] = errBoom
	problem, err = h.claims.Beat(ctx(), "A")
	require.NoError(t, err)
	assert.Equal(t, "heartbeat for session-A failed: boom", problem)

	_, err = h.claims.Beat(ctx(), "a b")
	assert.ErrorContains(t, err, "is not an owner token")
}

func TestWho(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		stored int64
		ago    int64
		want   string
	}{
		"live":       {stored: model.LeaseNone, ago: 10, want: "#5 held by session-B"},
		"expired":    {stored: model.LeaseNone, ago: lease + 60, want: "#5 held by session-B (LEASE EXPIRED: last seen 2h01m ago; `take` reclaims it)"},
		"unreadable": {stored: model.LeaseUnreadable, ago: 10, want: "#5 held by session-B (lease unreadable: clear the row by hand)"},
		"own_lease":  {stored: 5, ago: 10, want: "#5 held by session-B (LEASE EXPIRED: last seen 0h00m ago; `take` reclaims it)"},
	}

	for name, tt := range tests {
		h := newHarness(t)
		h.hold("5", sessionB, tt.stored, tt.ago)

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

	h.hold("5", sessionB, model.LeaseNone, 0)
	h.rows.extra = []model.ClaimRow{{Item: "5", Holder: sessionC}, {Item: "6", Holder: sessionA}, {Item: "5", Holder: sessionC}}
	got, err = h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 held by session-B, session-C (MORE THAN ONE HOLDER: resolve by hand)", got)
}

func TestRelease(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, lease, 0)
	require.NoError(t, h.claims.Release(ctx(), "A", "5"))
	assert.Equal(t, "released #5\n", h.said())
	assert.Empty(t, h.rows.rows)

	h = newHarness(t)
	require.NoError(t, h.claims.Release(ctx(), "A", "5"))
	assert.Equal(t, "#5 was not locked\n", h.said())
	assert.Zero(t, h.rows.calls["Free"])

	h.hold("5", sessionB, lease, 0)
	err := h.claims.Release(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 is held by session-B, not session-A")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Contains(t, h.rows.rows, "5")
}

func TestRelease_ThatDidNotTakeEffectIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, lease, 0)
	h.rows.freeKeeps = true

	err := h.claims.Release(ctx(), "A", "5")
	require.EqualError(t, err, `claim: release of #5 did not take effect: still held by "session-A"`)
	assert.Equal(t, claim.CodeRefused, h.code(err))
	assert.Empty(t, h.said())
}

func TestEveryReadAndWriteThatFailsIsARefusalThatKeepsItsCause(t *testing.T) {
	t.Parallel()

	steps := map[string]func(h *harness) error{
		"take":         func(h *harness) error { return h.claims.Take(ctx(), "A", "5") },
		"release":      func(h *harness) error { return h.claims.Release(ctx(), "A", "5") },
		"ensure_free":  func(h *harness) error { return h.claims.EnsureFree(ctx(), "A", "5") },
		"who":          func(h *harness) error { _, err := h.claims.Who(ctx(), "5"); return err },
		"take_expired": func(h *harness) error { return h.claims.Take(ctx(), "A", "5") },
	}
	tests := []struct{ step, method string }{
		{"take", "HeldRows"},
		{"take", "Row"},
		{"take", "TryHold"},
		{"take", "StoreLease"},
		{"release", "HeldRows"},
		{"release", "Free"},
		{"release", "Row"},
		{"ensure_free", "HeldRows"},
		{"ensure_free", "LastSeen"},
		{"who", "HeldRows"},
		{"who", "LastSeen"},
		{"take_expired", "Free"},
		{"take_expired", "LastSeen"},
		{"take_expired", "Row"},
	}

	for _, tt := range tests {
		h := newHarness(t)
		if tt.step == "take_expired" || tt.step == "ensure_free" || tt.step == "who" {
			h.hold("5", sessionB, model.LeaseNone, lease+1)
		}

		if tt.step == "release" {
			h.hold("5", sessionA, lease, 0)
		}

		h.rows.fail[tt.method] = errBoom
		err := steps[tt.step](h)

		require.ErrorIs(t, err, errBoom, tt.step+" "+tt.method)
		assert.Equal(t, claim.CodeRefused, h.code(err), tt.step+" "+tt.method)
	}
}

func TestAHeldRowsReadFailsAfterTheFirstReadIsStillARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, model.LeaseNone, lease+1)
	h.rows.onCall = func(method string, call int) {
		if method == "HeldRows" && call == 3 {
			h.rows.fail["HeldRows"] = errBoom
		}
	}

	err := h.claims.Take(ctx(), "A", "5")
	require.ErrorIs(t, err, errBoom)
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

func TestNew_RefusesWhatWouldLetASecondRunTakeALiveHold(t *testing.T) {
	t.Parallel()

	good := claim.Options{
		Rows: newFakeRows(), Lease: model.DefaultClaimLease, Settle: model.DefaultClaimSettle,
		Clock: time.Now, Sleep: time.Sleep, Nonce: claim.RandomNonce, Out: &bytes.Buffer{}, Warn: &bytes.Buffer{},
	}
	tests := map[string]struct {
		change func(*claim.Options)
		want   string
	}{
		"zero_lease":      {func(o *claim.Options) { o.Lease = 0 }, "the lease must be at least 1s, got 0s"},
		"sub_second":      {func(o *claim.Options) { o.Lease = 999 * time.Millisecond }, "the lease must be at least 1s"},
		"negative_settle": {func(o *claim.Options) { o.Settle = -time.Second }, "the settle pause must not be negative"},
		"no_rows":         {func(o *claim.Options) { o.Rows = nil }, "the claim rows is required"},
		"no_clock":        {func(o *claim.Options) { o.Clock = nil }, "the clock is required"},
		"no_sleep":        {func(o *claim.Options) { o.Sleep = nil }, "the sleep function is required"},
		"no_nonce":        {func(o *claim.Options) { o.Nonce = nil }, "the nonce source is required"},
		"no_out":          {func(o *claim.Options) { o.Out = nil }, "the output writer is required"},
		"no_warn":         {func(o *claim.Options) { o.Warn = nil }, "the warning writer is required"},
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

	good.Lease = time.Second
	_, err = claim.New(good)
	require.NoError(t, err)
}

func TestDefaults_AreTwoHoursAndTwoSeconds(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 7200*time.Second, model.DefaultClaimLease)
	assert.Equal(t, 2*time.Second, model.DefaultClaimSettle)
}

func TestError_UnwrapsItsCause(t *testing.T) {
	t.Parallel()

	failure := &claim.Error{Code: claim.CodeRefused, Message: "m", Err: errBoom}
	assert.ErrorIs(t, failure, errBoom)
}

func TestEnsureFree_ASecondHeldRowsReadThatFailsIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, model.LeaseNone, 5)
	h.rows.onCall = func(method string, call int) {
		if method == "HeldRows" && call == 2 {
			h.rows.fail["HeldRows"] = errBoom
		}
	}

	require.ErrorIs(t, h.claims.EnsureFree(ctx(), "A", "5"), errBoom)
}

func TestRandomNonce_IsNewEachTime(t *testing.T) {
	t.Parallel()

	first, second := claim.RandomNonce(), claim.RandomNonce()

	assert.NotEmpty(t, first)
	assert.NotEqual(t, first, second)
}

func TestRelease_OfARowThatChangedHandsAfterTheReadIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, lease, 0)
	h.rows.onCall = func(method string, _ int) {
		if method == "Free" {
			h.rows.rows["5"] = model.ClaimRow{Item: "5", Holder: sessionA, Nonce: "newer"}
		}
	}

	err := h.claims.Release(ctx(), "A", "5")

	require.EqualError(t, err, "REFUSED: #5 was claimed by another owner in the same window. Do not touch it.")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Equal(t, "newer", h.rows.rows["5"].Nonce)
}

func TestRelease_ARowReadThatFailsAfterTheFreeIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, lease, 0)
	h.rows.onCall = func(method string, call int) {
		if method == "Row" && call == 2 {
			h.rows.fail["Row"] = errBoom
		}
	}

	err := h.claims.Release(ctx(), "A", "5")

	require.ErrorIs(t, err, errBoom)
	assert.Empty(t, h.said())
}

func TestTake_AStoreLeaseThatFindsAnotherTakeIsRefusedWithCodeThree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.fail["StoreLease"] = model.ErrRowMoved

	err := h.claims.Take(ctx(), "A", "5")

	require.EqualError(t, err, "REFUSED: #5 was claimed by another owner in the same window.")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.NotContains(t, h.said(), "TAKEN")
}

func TestTake_ARowThatChangedHandsAfterTheLeaseWasStoredIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.onCall = func(method string, _ int) {
		if method == "StoreLease" {
			h.rows.onCall = func(next string, _ int) {
				if next == "Row" {
					h.rows.rows["5"] = model.ClaimRow{Item: "5", Holder: sessionA, Nonce: "newer"}
				}
			}
		}
	}

	err := h.claims.Take(ctx(), "A", "5")

	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.NotContains(t, h.said(), "TAKEN")
}

func TestTake_ARowReadThatFailsAfterTheLeaseWasStoredIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.rows.onCall = func(method string, _ int) {
		if method == "StoreLease" {
			h.rows.fail["Row"] = errBoom
		}
	}

	err := h.claims.Take(ctx(), "A", "5")

	require.ErrorIs(t, err, errBoom)
	assert.Equal(t, claim.CodeRefused, h.code(err))
}

func TestTake_AReclaimWhoseFreeFindsAnotherTakeIsRefusedWithCodeThree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, model.LeaseNone, lease+1)
	h.rows.fail["Free"] = model.ErrRowMoved

	err := h.claims.Take(ctx(), "A", "5")

	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.NotContains(t, h.said(), "reclaiming")
}

func TestRelease_OfARowThatChangedHandsBeforeTheFreeReadIsRefusedOrAlreadyFree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, lease, 0)
	h.rows.onCall = func(method string, _ int) {
		if method == "Row" {
			h.rows.rows["5"] = model.ClaimRow{Item: "5", Holder: sessionB}
		}
	}

	err := h.claims.Release(ctx(), "A", "5")

	require.EqualError(t, err, "REFUSED: #5 is held by session-B, not session-A")
	assert.Zero(t, h.rows.calls["Free"])

	h = newHarness(t)
	h.hold("5", sessionA, lease, 0)
	h.rows.onCall = func(method string, _ int) {
		if method == "Row" {
			delete(h.rows.rows, "5")
		}
	}

	require.NoError(t, h.claims.Release(ctx(), "A", "5"))
	assert.Equal(t, "#5 was not locked\n", h.said())
}
