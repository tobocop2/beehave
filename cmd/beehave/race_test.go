package main

import (
	"bytes"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/claim"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store/beads"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
)

// The tests here run claim over the real beads claim store, which only the composition root may join.
// The fake bd is the one of internal/store/beadstest. It implements --claim, heartbeat, reclaim, --if-assignee and --if-status as bd does:
// a test calls a second claimer from a hook, between the read of the first claimer and its write.

const (
	oldHolder = "session-old"
	oldRow    = "p-lock-gh-5"
)

// claimsOn makes the claims of one run at the time now.
func claimsOn(t *testing.T, rows claim.Rows, now int64) (*claim.Claims, *bytes.Buffer) {
	t.Helper()

	out := &bytes.Buffer{}
	c, err := claim.New(claim.Options{Rows: rows, Clock: func() time.Time { return time.Unix(now, 0) }, Out: out})
	require.NoError(t, err)

	return c, out
}

func claimCode(t *testing.T, err error) int {
	t.Helper()

	var failure *claim.Error
	require.ErrorAs(t, err, &failure)

	return failure.Code
}

func beadsOver(t *testing.T, run proc.Runner) *beads.Store {
	t.Helper()

	bd, err := beads.New(run, beadstest.Block("p"))
	require.NoError(t, err)

	return bd
}

func heldBy(t *testing.T, rows claim.Rows, item string) string {
	t.Helper()

	held, err := rows.HeldRows(t.Context())
	require.NoError(t, err)

	i := slices.IndexFunc(held, func(row model.ClaimRow) bool { return row.Item == item })
	if i < 0 {
		return ""
	}

	return held[i].Holder
}

// updates reports a bd update that has the flag.
func updates(flag string) func([]string) bool {
	return func(args []string) bool { return args[0] == "update" && slices.Contains(args, flag) }
}

// reclaims reports a bd reclaim: the free of an ended lease.
func reclaims(args []string) bool { return args[0] == "reclaim" }

// clears reports the reset of a stale row: a bd update that sets the status back to open.
func clears(args []string) bool {
	return updates("--status")(args) && slices.Contains(args, "open")
}

// expiredRow makes a claim row held by oldHolder whose lease ended.
func expiredRow(bd *beadstest.Bd) {
	bd.Add(oldRow, beadstest.Fields{"title": "lock:gh-5", "status": beadstest.Progress, "assignee": oldHolder, "lease_expires_at": beadstest.At(beadstest.Now - 1)})
}

type raceResult struct {
	errB error
	outA string
	rows claim.Rows
}

// twoClaimers makes A and B over the same bd. B reaches the command that when accepts, and A takes the item in the gap.
func twoClaimers(t *testing.T, bd *beadstest.Bd, when func([]string) bool) raceResult {
	t.Helper()

	a, outA := claimsOn(t, beadsOver(t, bd), beadstest.Now)
	hook := &beadstest.Hook{Inner: bd, When: when, Fire: func() { require.NoError(t, a.Take(t.Context(), "a", "5")) }}
	b, _ := claimsOn(t, beadsOver(t, hook), beadstest.Now)

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

	bd := beadstest.NewBd(t)

	twoClaimers(t, bd, updates("--claim")).oneWinner(t)
}

func TestRace_AReclaimRacingAReclaimGivesOneWinner(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	expiredRow(bd)

	twoClaimers(t, bd, reclaims).oneWinner(t)
}

func TestRace_AClearOfAStaleAssigneeCannotWipeANewClaim(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Add(oldRow, beadstest.Fields{"title": "lock:gh-5", "status": "closed", "assignee": oldHolder})

	twoClaimers(t, bd, clears).oneWinner(t)
}

func TestRace_ABeatBeforeTheReclaimKeepsTheHold(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	expiredRow(bd)
	hook := &beadstest.Hook{Inner: bd, When: reclaims, Fire: func() { require.NoError(t, beadsOver(t, bd).Beat(t.Context(), oldHolder)) }}
	b, _ := claimsOn(t, beadsOver(t, hook), beadstest.Now)

	err := b.Take(t.Context(), "b", "5")

	require.Error(t, err, "bd reclaim must see the renewed lease")
	assert.Equal(t, claim.CodeHeld, claimCode(t, err))
	assert.Equal(t, oldHolder, bd.Rows[oldRow]["assignee"])
}

func TestRace_ABdWithoutTheGuardsRefusesTheClaim(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.NoGuard = true
	bd.Add(oldRow, beadstest.Fields{"title": "lock:gh-5", "status": "closed", "assignee": oldHolder})
	b, _ := claimsOn(t, beadsOver(t, bd), beadstest.Now)

	require.ErrorContains(t, b.Take(t.Context(), "b", "5"), "the guards need bd 1.3.2 or newer")
	assert.Equal(t, oldHolder, bd.Rows[oldRow]["assignee"], "nothing was cleared")
}

// adapterCase returns the claim store over a fake bd that can fail its reads.
func adapterCase(t *testing.T) (claim.Rows, *beadstest.Bd, *beadstest.Reads) {
	t.Helper()

	bd := beadstest.NewBd(t)
	reads := &beadstest.Reads{Inner: bd}

	return beadsOver(t, reads), bd, reads
}

func TestClaimOverBeads_TakeRefuseEndAndReclaim(t *testing.T) {
	t.Parallel()

	rows, bd, _ := adapterCase(t)
	first, firstOut := claimsOn(t, rows, beadstest.Now)
	require.NoError(t, first.Take(t.Context(), "A", "5"))
	assert.Contains(t, firstOut.String(), "TAKEN #5 by session-A")

	live, _ := claimsOn(t, rows, beadstest.Now+30)
	liveErr := live.Take(t.Context(), "B", "5")
	require.Error(t, liveErr, "a live hold is not taken")
	assert.Equal(t, claim.CodeHeld, claimCode(t, liveErr))

	bd.Rows[oldRow]["lease_expires_at"] = beadstest.At(beadstest.Now - 3600)
	later, laterOut := claimsOn(t, rows, beadstest.Now)
	who, err := later.Who(t.Context(), "5")
	require.NoError(t, err)
	assert.Contains(t, who, "LEASE ENDED 1h00m ago")

	require.NoError(t, later.Take(t.Context(), "B", "5"))
	assert.Contains(t, laterOut.String(), "reclaiming #5 from session-A")
	assert.Contains(t, laterOut.String(), "TAKEN #5 by session-B")
	assert.Equal(t, "session-B", heldBy(t, rows, "5"))
}

func TestClaimOverBeads_BeatKeepsTheHoldAndReleaseGivesTheItemBack(t *testing.T) {
	t.Parallel()

	rows, _, _ := adapterCase(t)
	first, out := claimsOn(t, rows, beadstest.Now)
	require.NoError(t, first.Take(t.Context(), "A", "5"))

	problem, err := first.Beat(t.Context(), "A")
	require.NoError(t, err)
	assert.Empty(t, problem)

	other, _ := claimsOn(t, rows, beadstest.Now)
	assert.Equal(t, claim.CodeHeld, claimCode(t, other.Release(t.Context(), "B", "5")), "another owner cannot release")

	require.NoError(t, first.Release(t.Context(), "A", "5"))
	assert.Contains(t, out.String(), "released #5")
	require.NoError(t, other.Take(t.Context(), "B", "5"))
	assert.Equal(t, "session-B", heldBy(t, rows, "5"))
}

func TestClaimOverBeads_AFailedReadIsARefusalAndChangesNothing(t *testing.T) {
	t.Parallel()

	rows, _, reads := adapterCase(t)
	first, _ := claimsOn(t, rows, beadstest.Now)
	require.NoError(t, first.Take(t.Context(), "A", "5"))

	reads.Down = true
	other, _ := claimsOn(t, rows, beadstest.Now+3600)
	err := other.Take(t.Context(), "B", "5")
	require.Error(t, err)
	assert.Equal(t, claim.CodeRefused, claimCode(t, err))

	_, err = other.Who(t.Context(), "5")
	require.Error(t, err)

	reads.Down = false
	assert.Equal(t, "session-A", heldBy(t, rows, "5"), "the hold of A stands")
}

// TestClaimOverBeads_AnyTrackerGetsBeadsClaims shows that the claim store does not depend on the tracker a project names.
func TestClaimOverBeads_AnyTrackerGetsBeadsClaims(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	table := newAdapters(bd, nil, nil)

	for _, tracker := range slices.Sorted(maps.Keys(table.trackers)) {
		rows, err := table.leases(t.Context(), model.Project{Name: "p", Repo: "o/n", Tracker: tracker, Forge: forgegithub.Name, Claims: beads.Claims, Blocks: beadstest.Blocks("p")})
		require.NoError(t, err, string(tracker))

		c, _ := claimsOn(t, rows, beadstest.Now)
		require.NoError(t, c.Take(t.Context(), "A", "5"), string(tracker))
		assert.Equal(t, "session-A", heldBy(t, rows, "5"), string(tracker))
		require.NoError(t, c.Release(t.Context(), "A", "5"), string(tracker))
	}
}
