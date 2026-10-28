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

func TestBeadsHeldRows_ReadsClaimRowsAndNothingElse(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.Add("p-lock-gh-5", fields{"title": "lock:gh-5 t", "status": progress, "assignee": "s1", "description": "x lease=60"})
	bd.Add("p-lock-bd-p-9", fields{"title": "lock:bd-p-9", "status": progress, "assignee": "s2"})
	bd.Add("p-x", fields{"title": "plain task", "status": progress, "assignee": "s3"})
	bd.Add("p-lock-gh-nokind", fields{"title": "lock:gh-zz t", "status": progress, "assignee": "s4"})
	bd.Add("p-lock-gh-6", fields{"title": "lock:gh-6 t", "status": "open", "assignee": ""})
	bd.Add("", fields{"title": "lock:gh-7 t", "status": progress, "assignee": "s5"})

	rows, err := beadsOver(t, bd).HeldRows(bg())
	require.NoError(t, err)
	assert.Equal(t, []model.ClaimRow{{Item: "5", Holder: "s1", Lease: 60}, {Item: "p-9", Holder: "s2", Lease: model.LeaseNone}}, rows)
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
		store := beadsOver(t, bd)

		_, err := store.HeldRows(bg())
		require.Error(t, err, name)
		assert.Equal(t, 2, codeOf(t, err), name)

		_, err = store.LastSeen(bg())
		require.Error(t, err, name)
	}
}

func TestBeadsLastSeen_ReadsOnlyThisPrefixWithAStamp(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.Add("p-agent-session-a", fields{"issue_type": "agent", "last_activity": "2026-10-01T00:00:00Z"})
	bd.Add("p-agent-session-b", fields{"issue_type": "agent", "last_activity": nil})
	bd.Add("q-agent-session-c", fields{"issue_type": "agent", "last_activity": "2026-10-01T00:00:00Z"})

	seen, err := beadsOver(t, bd).LastSeen(bg())
	require.NoError(t, err)
	assert.Equal(t, map[string]time.Time{"session-a": time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}, seen)
}

func TestBeadsBeat(t *testing.T) {
	t.Parallel()

	t.Run("makes_the_agent_bead_and_stamps_it", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		require.NoError(t, beadsOver(t, bd).Beat(bg(), "session-a"))
		assert.True(t, bd.Called("agent", "heartbeat", "p-agent-session-a"))
		assert.Equal(t, 1, bd.Count("create"))

		require.NoError(t, beadsOver(t, bd).Beat(bg(), "session-a"))
		assert.Equal(t, 1, bd.Count("create"))
	})

	t.Run("a_failed_heartbeat_is_an_error", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.HeartbeatFails = true
		require.ErrorContains(t, beadsOver(t, bd).Beat(bg(), "session-a"), "heartbeat for p-agent-session-a failed: heartbeat refused")
	})

	t.Run("a_bead_that_cannot_be_made_is_an_error", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Fail["create"] = model.CommandResult{ExitCode: 1}
		require.ErrorContains(t, beadsOver(t, bd).Beat(bg(), "session-a"), "could not create the agent bead p-agent-session-a")
	})

	t.Run("an_unreadable_show_is_the_error", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Fail["show"] = model.CommandResult{Stdout: "nope"}
		require.ErrorContains(t, beadsOver(t, bd).Beat(bg(), "session-a"), "cannot read the answer of `bd show`")
	})

	t.Run("a_show_that_fails_after_the_create_reports_the_show_error", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Hooks = append(bd.Hooks, func(args []string) {
			if args[0] == "create" {
				bd.Fail["show"] = model.CommandResult{Stdout: "nope"}
			}
		})
		require.ErrorContains(t, beadsOver(t, bd).Beat(bg(), "session-a"), "cannot read the answer of `bd show`")
	})
}

func TestBeadsRow(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.Add("p-lock-gh-5", fields{"status": progress, "assignee": "s1"})
	bd.Add("p-lock-gh-60", fields{"status": "closed"})
	bd.Add("p-lock-gh-7", fields{"status": "closed", "assignee": ""})
	beads := beadsOver(t, bd)

	state, err := beads.Row(bg(), "5")
	require.NoError(t, err)
	assert.Equal(t, model.RowState{Exists: true, Held: true, Holder: "s1"}, state)

	state, err = beads.Row(bg(), "6")
	require.NoError(t, err)
	assert.Equal(t, model.RowState{}, state, "a partial id resolves to the longer row, which is another row")

	state, err = beads.Row(bg(), "7")
	require.NoError(t, err)
	assert.Equal(t, model.RowState{Exists: true}, state)

	bd.Fail["show"] = model.CommandResult{ExitCode: 1, Stderr: "database locked"}
	_, err = beads.Row(bg(), "5")
	require.ErrorContains(t, err, "cannot read the row p-lock-gh-5: database locked")

	bd.Fail["show"] = model.CommandResult{Stdout: "[]"}
	state, err = beads.Row(bg(), "5")
	require.Error(t, err)
	assert.Equal(t, model.RowState{}, state)
}

func TestBeadsTryHold(t *testing.T) {
	t.Parallel()

	t.Run("makes_the_row_titled_from_a_beads_item", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-9", fields{"title": strings.Repeat("é", 80) + "  "})
		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "p-9", Holder: "s1", Lease: 60}))

		assert.Equal(t, "lock:bd-p-9 "+strings.Repeat("é", 70), bd.Rows["p-lock-bd-p-9"]["title"])
		assert.Equal(t, "s1", bd.Rows["p-lock-bd-p-9"]["assignee"])
		assert.Contains(t, bd.Rows["p-lock-bd-p-9"]["description"], "lease=60")
	})

	t.Run("a_forge_number_gets_a_bare_title_and_no_gh_call", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1", Lease: 60}))
		assert.Equal(t, "lock:gh-5", bd.Rows["p-lock-gh-5"]["title"])
	})

	t.Run("a_beads_item_that_cannot_be_shown_gets_a_bare_title", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "p-9", Holder: "s1", Lease: 60}))
		assert.Equal(t, "lock:bd-p-9", bd.Rows["p-lock-bd-p-9"]["title"])

		bd = newFakeBd(t)
		bd.Hooks = append(bd.Hooks, func(args []string) {
			if args[0] == "create" {
				bd.Fail["show"] = model.CommandResult{Stdout: "nope"}
			}
		})
		bd.Add("p-9", fields{"title": "t"})
		bd.Fail["show"] = model.CommandResult{Stdout: "nope"}
		require.Error(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "p-9", Holder: "s1", Lease: 60}))
	})

	t.Run("a_stale_assignee_on_a_row_that_holds_nothing_is_cleared_first", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"status": "closed", "assignee": "old"})
		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1", Lease: 60}))
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
				bd.Rows["p-lock-gh-5"]["assignee"] = "winner#T"
			}
		})

		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1", Nonce: "N"}))
		assert.Equal(t, "winner#T", bd.Rows["p-lock-gh-5"]["assignee"])
		assert.False(t, bd.Called("update", "p-lock-gh-5", "--claim", "--assignee", "s1#N"))
	})

	t.Run("an_open_row_with_no_assignee_is_claimed_without_a_clearing_update", func(t *testing.T) {
		t.Parallel()

		bd := newFakeBd(t)
		bd.Add("p-lock-gh-5", fields{"status": "open"})
		require.NoError(t, beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "s1", Lease: 60}))
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

func TestBeadsStoreLeaseAndFree_RefuseAFailedUpdate(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.Fail["update"] = model.CommandResult{ExitCode: 1, Stderr: "locked"}
	beads := beadsOver(t, bd)

	require.ErrorContains(t, beads.StoreLease(bg(), model.ClaimRow{Item: "5", Holder: "s1", Lease: 60}), "cannot update the row")

	bd.Add("p-lock-gh-5", fields{"status": progress, "assignee": "s1"})
	bd.Fail["unclaim"] = model.CommandResult{ExitCode: 1, Stderr: "locked"}
	require.ErrorContains(t, beads.Free(bg(), model.ClaimRow{Item: "5", Holder: "s1"}), "locked")
}
