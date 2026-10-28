package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/model/contract"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/storetest"
)

func bg() context.Context {
	return context.Background()
}

func beadsOver(t *testing.T, run store.Runner) *store.BeadsStore {
	t.Helper()

	bd, err := store.NewBeadsStore(run, "p")
	require.NoError(t, err)

	return bd
}

// leaseIn returns the lease end of a row that ends seconds after the fake clock of bd.
func leaseIn(seconds int64) string {
	return storetest.At(nowUnix + seconds)
}

func TestBeadsStore_ConformsToTracker(t *testing.T) {
	t.Parallel()

	contract.TestTracker(t, func(t *testing.T, broken bool) model.Tracker {
		t.Helper()

		if broken {
			return beadsOver(t, brokenRunner{})
		}

		return beadsOver(t, newFakeBd(t))
	})
}

func TestBeadsStore_ConformsToClaimRows(t *testing.T) {
	t.Parallel()

	contract.TestClaimRows(t, func(t *testing.T, broken bool) model.ClaimRows {
		t.Helper()

		if broken {
			return beadsOver(t, brokenRunner{})
		}

		return beadsOver(t, newFakeBd(t))
	})
}

func TestGithubStore_ConformsToTracker(t *testing.T) {
	t.Parallel()

	contract.TestTracker(t, func(t *testing.T, _ bool) model.Tracker {
		t.Helper()

		gh, err := store.NewGithubStore(ghRepo, brokenRunner{})
		require.NoError(t, err)

		return gh
	})
}

func TestNewBeadsStore_NeedsAnIDPrefix(t *testing.T) {
	t.Parallel()

	_, err := store.NewBeadsStore(&scripted{}, "")
	require.ErrorContains(t, err, "the beads id prefix is required")
	assert.Equal(t, store.CodeRefused, codeOf(t, err))
}

func TestBeadsHeldRows_ReadsClaimRowsAndTheEndOfTheirLease(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.Add("p-lock-gh-5", fields{"title": "lock:gh-5 t", "status": progress, "assignee": "s1", "lease_expires_at": leaseIn(60)})
	bd.Add("p-lock-bd-p-9", fields{"title": "lock:bd-p-9", "status": progress, "assignee": "s2"})
	bd.Add("p-x", fields{"title": "plain task", "status": progress, "assignee": "s3"})
	bd.Add("p-lock-gh-nokind", fields{"title": "lock:gh-zz t", "status": progress, "assignee": "s4"})
	bd.Add("p-lock-gh-6", fields{"title": "lock:gh-6 t", "status": "open", "assignee": ""})
	bd.Add("", fields{"title": "lock:gh-7 t", "status": progress, "assignee": "s5"})

	rows, err := beadsOver(t, bd).HeldRows(bg())
	require.NoError(t, err)
	assert.Equal(t, []model.ClaimRow{{Item: "5", Holder: "s1", LeaseEnds: time.Unix(nowUnix+60, 0).UTC()}, {Item: "p-9", Holder: "s2"}}, rows)
}

func TestBeadsHeldRows_ALeaseEndThatIsNoTimeIsARefusal(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.Add("p-lock-gh-5", fields{"title": "lock:gh-5", "status": progress, "assignee": "s1", "lease_expires_at": "soon"})

	_, err := beadsOver(t, bd).HeldRows(bg())
	require.ErrorContains(t, err, `the lease end "soon" of p-lock-gh-5 is not a time`)
	assert.Equal(t, store.CodeRefused, codeOf(t, err))
}

func TestBeadsReads_AreRefusalsWhenTheListCannotBeRead(t *testing.T) {
	t.Parallel()

	tests := map[string]model.CommandResult{
		"exit":      {ExitCode: 1, Stderr: "down"},
		"null":      {Stdout: "null"},
		"not_json":  {Stdout: "nope"},
		"full_page": {Stdout: toJSON(t, fill(5000, nil))},
	}

	for name, result := range tests {
		bd := newFakeBd(t)
		bd.Fail["list"] = result
		beads := beadsOver(t, bd)

		_, err := beads.HeldRows(bg())
		require.Error(t, err, name)
		assert.Equal(t, store.CodeRefused, codeOf(t, err), name)

		require.Error(t, beads.Beat(bg(), "s1"), name)
	}
}

func TestBeadsBeat(t *testing.T) {
	t.Parallel()

	t.Run("renews_the_lease_of_every_row_of_the_holder_as_that_holder", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"title": "lock:gh-5", "status": progress, "assignee": "s1", "lease_expires_at": leaseIn(-5)})
		bd.Add("p-lock-gh-6", fields{"title": "lock:gh-6", "status": progress, "assignee": "s1", "lease_expires_at": leaseIn(5)})
		bd.Add("p-lock-gh-7", fields{"title": "lock:gh-7", "status": progress, "assignee": "s2", "lease_expires_at": leaseIn(5)})

		require.NoError(t, beadsOver(t, bd).Beat(bg(), "s1"))
		assert.True(t, bd.Called("heartbeat", "p-lock-gh-5", "--actor", "s1"))
		assert.True(t, bd.Called("heartbeat", "p-lock-gh-6", "--actor", "s1"))
		assert.Equal(t, 2, bd.Count("heartbeat"), "the row of another holder is not renewed")
		assert.Equal(t, leaseIn(storetest.LeaseSeconds), bd.Rows["p-lock-gh-5"]["lease_expires_at"])
		assert.Equal(t, leaseIn(5), bd.Rows["p-lock-gh-7"]["lease_expires_at"])
	})

	t.Run("a_holder_without_a_row_has_lost_its_claim", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-7", fields{"title": "lock:gh-7", "status": progress, "assignee": "s2"})

		err := beadsOver(t, bd).Beat(bg(), "s1")
		require.ErrorIs(t, err, model.ErrNoClaim)
		assert.Zero(t, bd.Count("heartbeat"))
	})

	t.Run("a_row_that_fails_does_not_stop_the_renewal_of_the_others_and_every_error_is_reported", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"title": "lock:gh-5", "status": progress, "assignee": "s1", "lease_expires_at": leaseIn(5)})
		bd.Add("p-lock-gh-6", fields{"title": "lock:gh-6", "status": progress, "assignee": "s1", "lease_expires_at": leaseIn(5)})
		bd.Add("p-lock-gh-7", fields{"title": "lock:gh-7", "status": progress, "assignee": "s1", "lease_expires_at": leaseIn(5)})

		run := failingHeartbeats{inner: bd, rows: []string{"p-lock-gh-5", "p-lock-gh-7"}}

		err := beadsOver(t, run).Beat(bg(), "s1")
		require.ErrorContains(t, err, "heartbeat for p-lock-gh-5 failed: boom")
		require.ErrorContains(t, err, "heartbeat for p-lock-gh-7 failed: boom")
		assert.Equal(t, leaseIn(storetest.LeaseSeconds), bd.Rows["p-lock-gh-6"]["lease_expires_at"], "the row after the first failure is renewed")
	})

	t.Run("a_refused_heartbeat_is_an_error", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"title": "lock:gh-5", "status": progress, "assignee": "s1"})
		bd.Fail["heartbeat"] = model.CommandResult{ExitCode: 1, Stderr: "dolt server is not reachable"}

		err := beadsOver(t, bd).Beat(bg(), "s1")
		require.ErrorContains(t, err, "heartbeat for p-lock-gh-5 failed: dolt server is not reachable")
		require.NotErrorIs(t, err, model.ErrNoClaim)
		assert.Equal(t, store.CodeRefused, codeOf(t, err))
	})

	t.Run("a_row_that_another_holder_took_since_the_list_is_a_lost_claim", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"title": "lock:gh-5", "status": progress, "assignee": "s1"})
		bd.Fail["heartbeat"] = model.CommandResult{ExitCode: 1, Stderr: "issue already claimed by s2"}

		err := beadsOver(t, bd).Beat(bg(), "s1")
		require.ErrorIs(t, err, model.ErrNoClaim)
		require.ErrorContains(t, err, "p-lock-gh-5")
	})
}

func TestBeadsTryHold(t *testing.T) {
	t.Parallel()

	t.Run("makes_the_row_titled_from_a_beads_item_and_claims_it_as_the_holder", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-9", fields{"title": strings.Repeat("é", 80) + "  "})
		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "p-9", Holder: "s1"}))

		row := bd.Rows["p-lock-bd-p-9"]
		assert.Equal(t, "lock:bd-p-9 "+strings.Repeat("é", 70), row["title"])
		assert.Equal(t, "s1", row["assignee"])
		assert.Equal(t, leaseIn(storetest.LeaseSeconds), row["lease_expires_at"], "bd grants the lease to the actor of the claim")
		assert.True(t, bd.Called("update", "p-lock-bd-p-9", "--claim", "--actor", "s1"))
		assert.NotContains(t, bd.Calls[len(bd.Calls)-1], "--assignee", "a claim that sets the assignee ends the lease")
	})

	t.Run("a_forge_number_gets_a_bare_title_and_no_gh_call", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1"}))
		assert.Equal(t, "lock:gh-5", bd.Rows["p-lock-gh-5"]["title"])
	})

	t.Run("a_row_whose_id_only_starts_with_the_id_is_another_row", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5-extra", fields{"title": "other"})
		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1"}))
		assert.Equal(t, "s1", bd.Rows[rowA]["assignee"])
		assert.Equal(t, "open", bd.Rows["p-lock-gh-5-extra"]["status"])
	})

	t.Run("a_lost_take_is_no_error_and_leaves_the_winner", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		beads := beadsOver(t, bd)
		require.NoError(t, beads.TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1"}))
		require.NoError(t, beads.TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s2"}))
		assert.Equal(t, "s1", bd.Rows["p-lock-gh-5"]["assignee"])
	})

	t.Run("a_beads_item_with_no_row_gets_a_bare_title", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "p-9", Holder: "s1"}))
		assert.Equal(t, "lock:bd-p-9", bd.Rows["p-lock-bd-p-9"]["title"])
	})

	t.Run("a_show_that_fails_is_an_error", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-9", fields{"title": "t"})
		bd.Fail["show"] = model.CommandResult{Stdout: "nope"}
		require.Error(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "p-9", Holder: "s1"}))
	})

	t.Run("a_stale_assignee_on_a_row_that_holds_nothing_is_cleared_first", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"status": "closed", "assignee": "old"})
		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1"}))
		assert.True(t, bd.Called("update", "p-lock-gh-5", "--if-assignee", "old", "--if-status", "closed", "--status", "open", "--assignee", ""))
		assert.Equal(t, "s1", bd.Rows["p-lock-gh-5"]["assignee"])
	})

	t.Run("a_stale_clear_that_lost_to_another_claim_leaves_that_claim", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"status": "closed", "assignee": "old"})
		bd.Hooks = append(bd.Hooks, func(args []string) {
			if args[0] == "update" && slices.Contains(args, "--if-assignee") {
				bd.Rows["p-lock-gh-5"]["status"] = progress
				bd.Rows["p-lock-gh-5"]["assignee"] = "winner"
			}
		})

		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1"}))
		assert.Equal(t, "winner", bd.Rows["p-lock-gh-5"]["assignee"])
		assert.False(t, bd.Called("update", "p-lock-gh-5", "--claim", "--actor", "s1"))
	})

	t.Run("an_open_row_with_no_assignee_is_claimed_without_a_clearing_update", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"status": "open"})
		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1"}))
		assert.False(t, bd.Called("update", "p-lock-gh-5", "--if-assignee", "", "--assignee", ""))
	})

	t.Run("a_row_that_cannot_be_made_is_an_error", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Fail["create"] = model.CommandResult{ExitCode: 1}
		require.ErrorContains(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1"}), "could not create the claim row p-lock-gh-5")

		bd.Fail["create"] = model.CommandResult{}
		bd.Fail["show"] = model.CommandResult{Stdout: "nope"}
		require.Error(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1"}))
	})

	t.Run("a_failed_update_is_an_error", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"status": "closed", "assignee": "old"})
		bd.Fail["update"] = model.CommandResult{ExitCode: 1, Stderr: "locked"}
		require.ErrorContains(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1"}), "locked")

		bd.Add("p-lock-gh-5", fields{"status": "open"})
		require.ErrorContains(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1"}), "cannot claim the row")
	})
}

func TestBeadsReclaim(t *testing.T) {
	t.Parallel()

	t.Run("frees_a_row_whose_lease_ended_and_only_that_row", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"title": "lock:gh-5", "status": progress, "assignee": "s1", "lease_expires_at": leaseIn(-1)})
		bd.Add("p-lock-gh-6", fields{"title": "lock:gh-6", "status": progress, "assignee": "s2", "lease_expires_at": leaseIn(-1)})

		freed, err := beadsOver(t, bd).Reclaim(bg(), "5")
		require.NoError(t, err)
		assert.True(t, freed)
		assert.True(t, bd.Called("reclaim", "--id", "p-lock-gh-5", "--older-than", "0s", "--json"))
		assert.Equal(t, "open", bd.Rows["p-lock-gh-5"]["status"])
		assert.Equal(t, progress, bd.Rows["p-lock-gh-6"]["status"], "bd reclaim is scoped to the row of the item")
	})

	t.Run("leaves_a_row_whose_lease_has_not_ended", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"title": "lock:gh-5", "status": progress, "assignee": "s1", "lease_expires_at": leaseIn(1)})

		freed, err := beadsOver(t, bd).Reclaim(bg(), "5")
		require.NoError(t, err)
		assert.False(t, freed)
		assert.Equal(t, progress, bd.Rows["p-lock-gh-5"]["status"])
	})

	t.Run("a_failed_or_unreadable_reclaim_is_an_error", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Fail["reclaim"] = model.CommandResult{ExitCode: 1, Stderr: "locked"}
		_, err := beadsOver(t, bd).Reclaim(bg(), "5")
		require.ErrorContains(t, err, "cannot reclaim the row p-lock-gh-5: locked")

		bd.Fail["reclaim"] = model.CommandResult{Stdout: "nope"}
		_, err = beadsOver(t, bd).Reclaim(bg(), "5")
		require.ErrorContains(t, err, "cannot read the answer of `bd reclaim`")
	})
}

func TestBeadsFree(t *testing.T) {
	t.Parallel()

	t.Run("a_missing_row_is_free_and_another_holder_stays", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		beads := beadsOver(t, bd)
		require.NoError(t, beads.Free(bg(), model.ClaimRow{Item: "5", Holder: "session-a"}), "a missing row is free")

		bd.Add(rowA, fields{"title": "lock:gh-5", "status": progress, "assignee": "session-b"})
		require.ErrorIs(t, beads.Free(bg(), model.ClaimRow{Item: "5", Holder: "session-a"}), model.ErrRowMoved)
		assert.Equal(t, "session-b", bd.Rows[rowA]["assignee"])

		bd.Fail["unclaim"] = model.CommandResult{ExitCode: 1, Stderr: "locked"}
		require.ErrorContains(t, beads.Free(bg(), model.ClaimRow{Item: "5", Holder: "session-b"}), "locked")

		bd.Fail["show"] = model.CommandResult{ExitCode: 1, Stderr: "disk error"}
		require.ErrorContains(t, beads.Free(bg(), model.ClaimRow{Item: "5", Holder: "session-b"}), "disk error")
	})

	t.Run("a_row_freed_meanwhile_is_free", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add(rowA, fields{"title": "lock:gh-5", "status": "closed"})

		require.NoError(t, beadsOver(t, bd).Free(bg(), model.ClaimRow{Item: "5", Holder: "session-a"}))
	})

	t.Run("guards_the_release_by_the_holder_and_ends_the_lease", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		beads := beadsOver(t, bd)
		row := model.ClaimRow{Item: "5", Holder: "session-a"}
		require.NoError(t, beads.TryHold(bg(), row))
		require.NoError(t, beads.Free(bg(), row))

		assert.True(t, bd.Called("unclaim", rowA, "--if-assignee", "session-a", "--actor", "session-a"))
		assert.NotContains(t, bd.Rows[rowA], "lease_expires_at")
	})
}

func TestBeadsMissingRowText_BothVersionsOfBdAreRecognised(t *testing.T) {
	t.Parallel()

	for name, v1 := range map[string]bool{"v0.56.1": false, "v1.3.2": true} {
		bd := newFakeBd(t)
		bd.V1 = v1

		require.NoError(t, beadsOver(t, bd).Free(bg(), model.ClaimRow{Item: "5", Holder: "s1"}), name)
	}

	bd := newFakeBd(t)
	bd.Fail["show"] = model.CommandResult{ExitCode: 1, Stderr: "no issues found matching the provided IDs"}
	bd.Fail["unclaim"] = model.CommandResult{ExitCode: 1, Stderr: "locked"}
	require.NoError(t, beadsOver(t, bd).Free(bg(), model.ClaimRow{Item: "5", Holder: "s1"}))
}
