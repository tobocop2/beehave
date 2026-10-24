package main

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/claim"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/storetest"
)

// The tests here run claim over the real beads lease store, which only the composition root may join.
// The fake bd is the one of internal/store/storetest. It implements --claim, --if-assignee and --if-status as bd does:
// a test calls a second claimer from a hook, between the read of the first claimer and its write.

const (
	oldHolder = "session-old"
	oldRow    = "p-lock-gh-5"
	oldAgent  = "p-agent-session-old"
)

type nonces struct {
	tag   string
	count int
}

func (n *nonces) next() string {
	n.count++

	return fmt.Sprintf("%s%d", n.tag, n.count)
}

// claimsOn makes the claims of one run at the time now. The tag keeps the tokens of two runs apart.
func claimsOn(t *testing.T, rows claim.Rows, now int64, tag string) (*claim.Claims, *bytes.Buffer) {
	t.Helper()

	out := &bytes.Buffer{}
	c, err := claim.New(claim.Options{
		Rows: rows, Lease: time.Minute, Settle: 0, Clock: func() time.Time { return time.Unix(now, 0) },
		Sleep: func(time.Duration) {}, Nonce: (&nonces{tag: tag}).next, Out: out, Warn: &bytes.Buffer{},
	})
	require.NoError(t, err)

	return c, out
}

func claimCode(t *testing.T, err error) int {
	t.Helper()

	var failure *claim.Error
	require.ErrorAs(t, err, &failure)

	return failure.Code
}

func beadsOver(t *testing.T, run store.Runner) *store.BeadsStore {
	t.Helper()

	bd, err := store.NewBeadsStore(run, "p")
	require.NoError(t, err)

	return bd
}

func heldBy(t *testing.T, rows claim.Rows, item string) string {
	t.Helper()

	state, err := rows.Row(t.Context(), item)
	require.NoError(t, err)

	return state.Holder
}

// updates reports a bd update that has the flag.
func updates(flag string) func([]string) bool {
	return func(args []string) bool { return args[0] == "update" && slices.Contains(args, flag) }
}

// releases reports a bd unclaim: the release of a take.
func releases(args []string) bool { return args[0] == "unclaim" }

// clears reports the reset of a stale row: a bd update that sets the status back to open.
func clears(args []string) bool {
	return updates("--status")(args) && slices.Contains(args, "open")
}

// expiredRow makes a claim row held by oldHolder under one token, with no beat for a long time.
func expiredRow(bd *storetest.Bd) {
	bd.Add(oldRow, storetest.Fields{"title": "lock:gh-5", "status": storetest.Progress, "assignee": oldHolder + "#T1", "description": "lease=60"})
	bd.Add(oldAgent, storetest.Fields{"issue_type": "agent", "last_activity": time.Unix(storetest.Now-1000, 0).UTC().Format(time.RFC3339)})
}

type raceResult struct {
	errB error
	outA string
	rows claim.Rows
}

// twoClaimers makes A and B over the same bd. B reaches the command that when accepts, and A takes the item in the gap.
func twoClaimers(t *testing.T, bd *storetest.Bd, when func([]string) bool) raceResult {
	t.Helper()

	a, outA := claimsOn(t, beadsOver(t, bd), storetest.Now, "a")
	hook := &storetest.Hook{Inner: bd, When: when, Fire: func() { require.NoError(t, a.Take(t.Context(), "a", "5")) }}
	b, _ := claimsOn(t, beadsOver(t, hook), storetest.Now, "b")

	return raceResult{errB: b.Take(t.Context(), "b", "5"), outA: outA.String(), rows: beadsOver(t, bd)}
}

func (r raceResult) oneWinner(t *testing.T) {
	t.Helper()

	assert.Contains(t, r.outA, "TAKEN #5 by session-a")
	require.Error(t, r.errB, "both claimers won")
	assert.Equal(t, claim.CodeHeld, claimCode(t, r.errB))
	assert.Equal(t, "session-a", heldBy(t, r.rows, "5"))
}

func TestRace_ATakeRacingATakeGivesOneWinner(t *testing.T) {
	t.Parallel()

	bd := storetest.NewBd(t)

	twoClaimers(t, bd, updates("--claim")).oneWinner(t)
}

func TestRace_AReclaimRacingAReclaimGivesOneWinner(t *testing.T) {
	t.Parallel()

	bd := storetest.NewBd(t)
	expiredRow(bd)

	twoClaimers(t, bd, releases).oneWinner(t)
}

func TestRace_AReclaimThatAnotherReclaimClaimedBeforeTheClaimGivesOneWinner(t *testing.T) {
	t.Parallel()

	bd := storetest.NewBd(t)
	expiredRow(bd)

	twoClaimers(t, bd, updates("--claim")).oneWinner(t)
}

func TestRace_AClearOfAStaleAssigneeCannotWipeANewClaim(t *testing.T) {
	t.Parallel()

	bd := storetest.NewBd(t)
	bd.Add(oldRow, storetest.Fields{"title": "lock:gh-5", "status": "closed", "assignee": oldHolder + "#T1"})

	twoClaimers(t, bd, clears).oneWinner(t)
}

// retake plays the old holder: it releases its take and takes the item again, so the row is the same holder with a new token.
func retake(t *testing.T, bd *storetest.Bd) {
	t.Helper()

	old := beadsOver(t, bd)
	require.NoError(t, old.Free(t.Context(), model.ClaimRow{Item: "5", Holder: oldHolder, Nonce: "T1"}))
	require.NoError(t, old.TryHold(t.Context(), model.ClaimRow{Item: "5", Holder: oldHolder, Nonce: "T2", Lease: 60}))
	require.NoError(t, old.Beat(t.Context(), oldHolder))
}

func TestRace_AReleaseAndRetakeBySameHolderCannotBeUndoneByAStaleGuard(t *testing.T) {
	t.Parallel()

	bd := storetest.NewBd(t)
	expiredRow(bd)
	hook := &storetest.Hook{Inner: bd, When: releases, Fire: func() { retake(t, bd) }}
	b, _ := claimsOn(t, beadsOver(t, hook), storetest.Now, "b")

	err := b.Take(t.Context(), "b", "5")

	require.Error(t, err, "the stale guard of B closed the retake of the old holder")
	assert.Equal(t, claim.CodeHeld, claimCode(t, err))
	assert.Equal(t, storetest.Progress, bd.Rows[oldRow]["status"])
	assert.Equal(t, oldHolder+"#T2", bd.Rows[oldRow]["assignee"])
}

func TestRace_ABdWithoutTheGuardsRefusesTheClaim(t *testing.T) {
	t.Parallel()

	bd := storetest.NewBd(t)
	bd.NoGuard = true
	bd.Add(oldRow, storetest.Fields{"title": "lock:gh-5", "status": "closed", "assignee": oldHolder + "#T1"})
	b, _ := claimsOn(t, beadsOver(t, bd), storetest.Now, "b")

	require.ErrorContains(t, b.Take(t.Context(), "b", "5"), "the guards need bd 1.3.2 or newer")
	assert.Equal(t, oldHolder+"#T1", bd.Rows[oldRow]["assignee"], "nothing was cleared")
}

// adapterCase returns the lease store over a fake bd that can fail its reads.
func adapterCase(t *testing.T) (claim.Rows, *storetest.Reads) {
	t.Helper()

	reads := &storetest.Reads{Inner: storetest.NewBd(t)}

	return beadsOver(t, reads), reads
}

func TestClaimOverBeads_TakeRefuseExpireAndReclaim(t *testing.T) {
	t.Parallel()

	rows, _ := adapterCase(t)
	first, firstOut := claimsOn(t, rows, storetest.Now, "a")
	require.NoError(t, first.Take(t.Context(), "A", "5"))
	assert.Contains(t, firstOut.String(), "TAKEN #5 by session-A")

	live, _ := claimsOn(t, rows, storetest.Now+30, "b")
	liveErr := live.Take(t.Context(), "B", "5")
	require.Error(t, liveErr, "a live hold is not taken")
	assert.Equal(t, claim.CodeHeld, claimCode(t, liveErr))

	later, laterOut := claimsOn(t, rows, storetest.Now+3600, "b")
	who, err := later.Who(t.Context(), "5")
	require.NoError(t, err)
	assert.Contains(t, who, "LEASE EXPIRED")
	assert.NotContains(t, who, "session-A#", "a person never sees the token")

	require.NoError(t, later.Take(t.Context(), "B", "5"))
	assert.Contains(t, laterOut.String(), "reclaiming #5 from session-A")
	assert.Contains(t, laterOut.String(), "TAKEN #5 by session-B")
	assert.Equal(t, "session-B", heldBy(t, rows, "5"))
}

func TestClaimOverBeads_ReleaseGivesTheItemBack(t *testing.T) {
	t.Parallel()

	rows, _ := adapterCase(t)
	first, out := claimsOn(t, rows, storetest.Now, "a")
	require.NoError(t, first.Take(t.Context(), "A", "5"))

	other, _ := claimsOn(t, rows, storetest.Now, "b")
	assert.Equal(t, claim.CodeHeld, claimCode(t, other.Release(t.Context(), "B", "5")), "another owner cannot release")

	require.NoError(t, first.Release(t.Context(), "A", "5"))
	assert.Contains(t, out.String(), "released #5")
	require.NoError(t, other.Take(t.Context(), "B", "5"))
	assert.Equal(t, "session-B", heldBy(t, rows, "5"))
}

func TestClaimOverBeads_AFailedReadIsARefusalAndChangesNothing(t *testing.T) {
	t.Parallel()

	rows, reads := adapterCase(t)
	first, _ := claimsOn(t, rows, storetest.Now, "a")
	require.NoError(t, first.Take(t.Context(), "A", "5"))

	reads.Down = true
	expired, _ := claimsOn(t, rows, storetest.Now+3600, "b")
	err := expired.Take(t.Context(), "B", "5")
	require.Error(t, err)
	assert.Equal(t, claim.CodeRefused, claimCode(t, err))

	_, err = expired.Who(t.Context(), "5")
	require.Error(t, err)

	reads.Down = false
	assert.Equal(t, "session-A", heldBy(t, rows, "5"), "the hold of A stands")
}

// TestClaimOverBeads_AnyTrackerGetsBeadsLeases shows that the lease store does not depend on the tracker a project names.
func TestClaimOverBeads_AnyTrackerGetsBeadsLeases(t *testing.T) {
	t.Parallel()

	bd := storetest.NewBd(t)
	table := newAdapters(bd)

	for _, tracker := range slices.Sorted(maps.Keys(table.trackers)) {
		rows, err := table.leases(model.Project{Name: "p", Repo: "o/n", Tracker: tracker, Forge: model.ForgeGithub, BeadsPrefix: "p"})
		require.NoError(t, err, string(tracker))

		c, _ := claimsOn(t, rows, storetest.Now, "a")
		require.NoError(t, c.Take(t.Context(), "A", "5"), string(tracker))
		assert.Equal(t, "session-A", heldBy(t, rows, "5"), string(tracker))
		require.NoError(t, c.Release(t.Context(), "A", "5"), string(tracker))
	}
}
