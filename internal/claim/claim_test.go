package claim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
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

func TestKindOf(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"2021": "gh", "0": "gh", "proj-a1b.2": "bd", "a-b": "bd", "A_1-x_y.z-9": "bd",
		"": "", "abc": "", "-x": "", "a-": "", "12a": "", "a b-c": "", "a-b c": "", "a/b-c": "", "٣": "", "_a-b": "", "a.b-c": "", "é-b": "",
	}
	for item, want := range tests {
		assert.Equal(t, want, claim.KindOf(item), item)
	}
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

func TestTake_MakesTheAgentBeadAndTheRowAndClaimsIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.gh.result = model.CommandResult{Stdout: "Parser drops the last row\n"}
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))

	assert.Equal(t, "TAKEN #5 by session-A\n", h.said())
	assert.Equal(t, progress, h.bd.rows[rowGH5]["status"])
	assert.Equal(t, sessionA, h.bd.rows[rowGH5]["assignee"])
	assert.Equal(t, "lock:gh-5 Parser drops the last row", h.bd.rows[rowGH5]["title"])
	assert.Equal(t, "2026-10-01T00:00:00Z", h.bd.rows["p-agent-session-A"]["last_activity"])
	assert.Equal(t, []time.Duration{500 * time.Millisecond}, h.slept)
	assert.Equal(t, [][]string{{"issue", "view", "5", "--repo", "owner/name", "--json", "title", "--jq", ".title"}}, h.gh.calls)
	assert.Contains(t, h.bd.rows[rowGH5]["description"], "lease="+strconv.Itoa(lease))
}

func TestTake_ABeadsItemGetsABdRowTitledFromTheItem(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.add("p-9.1", fields{"title": "child task"})
	require.NoError(t, h.claims.Take(ctx(), "A", "p-9.1"))

	assert.Equal(t, "lock:bd-p-9.1 child task", h.bd.rows["p-lock-bd-p-9.1"]["title"])
	assert.Equal(t, sessionA, h.bd.rows["p-lock-bd-p-9.1"]["assignee"])
	assert.Empty(t, h.gh.calls)
}

func TestTake_TheRowTitleHolds70CharactersAndNoTrailingBlank(t *testing.T) {
	t.Parallel()

	long := newHarness(t)
	long.gh.result = model.CommandResult{Stdout: strings.Repeat("x", 69) + " yyyy\n"}
	require.NoError(t, long.claims.Take(ctx(), "A", "5"))
	assert.Equal(t, "lock:gh-5 "+strings.Repeat("x", 69), long.bd.rows[rowGH5]["title"])

	failed := newHarness(t)
	failed.gh.result = model.CommandResult{ExitCode: 1, Stdout: "ignored"}
	require.NoError(t, failed.claims.Take(ctx(), "A", "5"))
	assert.Equal(t, "lock:gh-5", failed.bd.rows[rowGH5]["title"])
}

func TestTake_ABeadsItemThatBdCannotShowGivesABareRowTitle(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, h.claims.Take(ctx(), "A", "p-404"))
	assert.Equal(t, "lock:bd-p-404", h.bd.rows["p-lock-bd-p-404"]["title"])

	h.bd.add("p-7", fields{"title": nil})
	require.NoError(t, h.claims.Take(ctx(), "B", "p-7"))
	assert.Equal(t, "lock:bd-p-7", h.bd.rows["p-lock-bd-p-7"]["title"])
}

func TestTake_OfAnItemYouHoldChangesNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, "")
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Equal(t, "#5 already held by you\n", h.said())
	assert.Zero(t, h.bd.count("update"))
}

func TestTake_RefusesALiveHolderWithCodeThree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, "")
	h.seen(sessionB, 10)

	err := h.claims.Take(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 is held by session-B. Do not touch it.")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Zero(t, h.bd.count("update"))
}

func TestEnsureFree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, "")
	h.seen(sessionB, 10)

	err := h.claims.EnsureFree(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 is held by session-B. Do not touch it.")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Zero(t, h.bd.count("update"))

	for name, setup := range map[string]func(*harness){
		"unowned": func(*harness) {},
		"own":     func(h *harness) { h.hold("5", sessionA, ""); h.seen(sessionA, 10) },
		"expired": func(h *harness) { h.hold("5", sessionB, ""); h.seen(sessionB, lease+1) },
	} {
		free := newHarness(t)
		setup(free)
		require.NoError(t, free.claims.EnsureFree(ctx(), "A", "5"), name)
		assert.Zero(t, free.bd.count("update"), name)
	}
}

func TestEnsureFree_FailsClosedAndChecksNames(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.fail["list"] = model.CommandResult{Stdout: "garbage"}
	require.Error(t, h.claims.EnsureFree(ctx(), "A", "5"))

	require.ErrorContains(t, h.claims.EnsureFree(ctx(), "a-b", "5"), "owner token")
	require.ErrorContains(t, h.claims.EnsureFree(ctx(), "A", "x y"), "neither a forge number")

	other := newHarness(t)
	other.hold("5", sessionB, "")
	other.seen(sessionB, 10)
	other.bd.hooks = append(other.bd.hooks, func(args []string) {
		if args[0] == "list" && slices.Contains(args, "--type") {
			other.bd.fail["list"] = model.CommandResult{Stdout: "garbage"}
		}
	})
	require.Error(t, other.claims.EnsureFree(ctx(), "A", "5"))

	late := newHarness(t)
	late.hold("5", sessionB, "")
	late.seen(sessionB, 10)

	calls := 0
	late.bd.hooks = append(late.bd.hooks, func(args []string) {
		if args[0] == "list" {
			calls++
			if calls == 3 {
				late.bd.fail["list"] = model.CommandResult{Stdout: "garbage"}
			}
		}
	})
	require.Error(t, late.claims.EnsureFree(ctx(), "A", "5"))
}

func TestTake_AHolderWithNoAgentBeadOrNoStampNeverExpires(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, "")
	require.ErrorContains(t, h.claims.Take(ctx(), "A", "5"), "held by session-B")
	h.seen(sessionB, -1)
	require.ErrorContains(t, h.claims.Take(ctx(), "A", "5"), "held by session-B")
}

func TestTake_TheLeaseExpiresOneSecondPastItAndNotOnIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, "")
	h.seen(sessionB, lease)
	require.ErrorContains(t, h.claims.Take(ctx(), "A", "5"), "held by session-B")

	h.hold("6", sessionB, "")
	h.seen(sessionB, lease+1)
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Equal(t, "reclaiming #5 from session-B: last seen 2h00m ago, lease 2h00m\nTAKEN #5 by session-A\n", h.said())
	assert.Equal(t, progress, h.bd.rows["p-lock-gh-6"]["status"])
}

func TestTake_AReaderExpiresAHoldByTheLeaseTheHolderStored(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	id := h.hold("5", sessionB, "")
	h.bd.rows[id]["description"] = "Claim for 5. lease=1209600"
	h.seen(sessionB, 3*3600)

	who, err := h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 held by session-B", who)
	require.ErrorContains(t, h.claims.Take(ctx(), "A", "5"), "held by session-B")

	h.seen(sessionB, 1209600+1)

	who, err = h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Contains(t, who, "LEASE EXPIRED")
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.True(t, strings.HasPrefix(h.said(), "reclaiming #5 from session-B: last seen 336h00m ago, lease 336h00m"))
}

func TestWho_ARowWithAnUnreadableLeaseNeverExpires(t *testing.T) {
	t.Parallel()

	texts := []string{"lease=abc", "lease=-5", "lease=0", "lease=", "lease=1e9", "lease=12x", "lease= 9999999", "lease=" + strings.Repeat("9", 5000), "lease=" + strings.Repeat("1", 13), "lease=000"}
	for _, text := range texts {
		h := newHarness(t)
		id := h.hold("5", sessionB, "")
		h.bd.rows[id]["description"] = "Claim for 5. " + text
		h.seen(sessionB, 10*lease)

		who, err := h.claims.Who(ctx(), "5")
		require.NoError(t, err)
		assert.Equal(t, "#5 held by session-B (lease unreadable: clear the row by hand)", who, text)
		require.ErrorContains(t, h.claims.Take(ctx(), "A", "5"), "held by session-B", text)
	}
}

func TestWho_LeaseReading(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"twelve_digits_are_readable":    "Claim. lease=" + strings.Repeat("9", 12),
		"the_word_must_start_at_a_gap":  "Claim. xlease=1 lease=" + strings.Repeat("9", 12),
		"a_lease_at_the_start":          "lease=" + strings.Repeat("9", 12) + " trailing words",
		"a_lease_after_an_underscore":   "a_lease=1 lease=" + strings.Repeat("9", 12),
		"a_lease_followed_by_more_text": "Claim. lease=" + strings.Repeat("9", 12) + "\nmore",
	}
	for name, description := range tests {
		h := newHarness(t)
		id := h.hold("5", sessionB, "")
		h.bd.rows[id]["description"] = description
		h.seen(sessionB, 10*lease)

		who, err := h.claims.Who(ctx(), "5")
		require.NoError(t, err, name)
		assert.Equal(t, "#5 held by session-B", who, name)
	}
}

func TestWho_ARowWithNoStoredLeaseUsesTheReadersLease(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, "")
	h.seen(sessionB, lease+1)

	who, err := h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 held by session-B (LEASE EXPIRED: last seen 2h00m ago; `take` reclaims it)", who)
}

func TestTake_AnExpiredRowIsReopenedOnlyWhenTheHolderStillHoldsIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, "")
	h.seen(sessionB, lease+60)

	done := false
	h.bd.hooks = append(h.bd.hooks, func(args []string) {
		if !done && args[0] == "show" && args[1] == rowGH5 && h.bd.rows[rowGH5]["assignee"] == sessionB {
			h.bd.rows[rowGH5]["assignee"] = "session-C"
			done = true
		}
	})

	err := h.claims.Take(ctx(), "A", "5")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.NotContains(t, h.said(), "reclaiming")
	assert.Equal(t, "session-C", h.bd.rows[rowGH5]["assignee"])
}

func TestTake_ARowForItem99IsNotTakenForTheRowOfItem999999(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("999999", sessionB, "")
	h.seen(sessionB, 10)
	require.NoError(t, h.claims.Take(ctx(), "A", "99"))
	assert.Equal(t, sessionA, h.bd.rows["p-lock-gh-99"]["assignee"])
	assert.Equal(t, sessionB, h.bd.rows["p-lock-gh-999999"]["assignee"])
}

func TestTake_AClosedRowWithAStaleAssigneeIsClearedBeforeTheClaim(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.add(rowGH5, fields{"title": "lock:gh-5", "status": "closed", "assignee": sessionB})
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Equal(t, sessionA, h.bd.rows[rowGH5]["assignee"])
	assert.True(t, h.bd.called("update", rowGH5, "--assignee", ""))
}

func TestTake_AnOpenRowWithNoAssigneeIsClaimedWithoutAClearingUpdate(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.add(rowGH5, fields{"title": "lock:gh-5"})
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.False(t, h.bd.called("update", rowGH5, "--assignee", ""))
	assert.Equal(t, 1, h.bd.count("create"))
}

func TestTake_ARowThatCannotBeCreatedIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.fail["create"] = model.CommandResult{ExitCode: 1}

	err := h.claims.Take(ctx(), "A", "5")
	require.EqualError(t, err, "claim: could not create the claim row p-lock-gh-5")
	assert.Equal(t, claim.CodeRefused, h.code(err))
}

func TestTake_AConcurrentCreateThatResetsTheRowMakesTheTakeClaimAgain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	reset := false
	h.bd.hooks = append(h.bd.hooks, func(args []string) {
		if !reset && args[0] == "show" && args[1] == rowGH5 && h.bd.rows[rowGH5] != nil && h.bd.rows[rowGH5]["assignee"] == sessionA {
			reset = true
			h.bd.rows[rowGH5]["status"], h.bd.rows[rowGH5]["assignee"] = "open", ""
		}
	})
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Equal(t, "TAKEN #5 by session-A\n", h.said())
	assert.GreaterOrEqual(t, h.bd.count("update"), 2)
}

func TestTake_ARowThatKeepsResettingStopsAfterFourAttempts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.hooks = append(h.bd.hooks, func(args []string) {
		if args[0] == "show" && args[1] == rowGH5 && h.bd.rows[rowGH5] != nil {
			h.bd.rows[rowGH5]["status"], h.bd.rows[rowGH5]["assignee"] = "open", ""
		}
	})

	err := h.claims.Take(ctx(), "A", "5")
	require.EqualError(t, err, "claim: could not settle ownership of #5 after 4 attempts; check `obee claim who 5`")
	assert.Equal(t, claim.CodeRefused, h.code(err))

	claims := 0

	for _, call := range h.bd.calls {
		if slices.Contains(call, "--claim") {
			claims++
		}
	}

	assert.Equal(t, 4, claims)
}

func TestTake_AClaimThatLostTheRaceNamesTheWinner(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.hooks = append(h.bd.hooks, func(args []string) {
		if slices.Contains(args, "--claim") && h.bd.rows[rowGH5]["assignee"] == nil {
			h.bd.rows[rowGH5]["status"], h.bd.rows[rowGH5]["assignee"] = progress, sessionB
		}
	})

	err := h.claims.Take(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 was claimed by session-B in the same window.")
	assert.Equal(t, claim.CodeHeld, h.code(err))
}

func TestTake_ARowTakenOverDuringTheSettlePauseIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.sleep = func(time.Duration) { h.bd.rows[rowGH5]["assignee"] = sessionB }

	err := h.claims.Take(ctx(), "A", "5")
	require.ErrorContains(t, err, "was claimed by session-B in the same window")
	assert.Equal(t, claim.CodeHeld, h.code(err))
}

func TestTake_AFindOfASecondHolderAfterSettlingYields(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.sleep = func(time.Duration) { h.hold("5", sessionB, "-twin") }

	err := h.claims.Take(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 is also held by session-B; yielded.")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Equal(t, "closed", h.bd.rows[rowGH5]["status"])
	assert.Empty(t, h.bd.rows[rowGH5]["assignee"])
	assert.Empty(t, h.said())
}

func TestTake_AFailedHeartbeatWarnsAndTheTakeGoesOn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.heartbeatFails = true
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Equal(t, "WARNING: heartbeat for p-agent-session-A failed: heartbeat refused. Other runs will read this owner's claims as expired after 2h00m.\n", h.warn.String())
	assert.Equal(t, "TAKEN #5 by session-A\n", h.said())
}

func TestBeat(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.fail["create"] = model.CommandResult{ExitCode: 1}

	problem, err := h.claims.Beat(ctx(), "A")
	require.NoError(t, err)
	assert.Equal(t, "could not create the agent bead p-agent-session-A", problem)

	ok := newHarness(t)
	for range 2 {
		problem, err = ok.claims.Beat(ctx(), "A")
		require.NoError(t, err)
		assert.Empty(t, problem)
	}

	assert.Equal(t, 1, ok.bd.count("create"))
	assert.Equal(t, 2, ok.bd.count("agent"))
	assert.True(t, ok.bd.called("create", "--id", "p-agent-session-A", "--title", "lock owner session-A", "--type", "agent", "--labels", "gt:agent", "--silent",
		"--description", "Liveness for the claim: last_activity is this owner's last heartbeat."))

	_, err = ok.claims.Beat(ctx(), "a-b")
	assert.ErrorContains(t, err, "owner token")
}

func TestBeat_AShowThatPrintsSomethingElseIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.fail["show"] = model.CommandResult{Stdout: "not json"}

	_, err := h.claims.Beat(ctx(), "A")
	require.ErrorContains(t, err, "claim: cannot parse `bd show p-agent-session-A` (")
	require.ErrorContains(t, err, "); refusing to answer")

	err = h.claims.Take(ctx(), "A", "5")
	require.ErrorContains(t, err, "claim: cannot parse `bd show p-agent-session-A`")

	h.bd.fail["show"] = model.CommandResult{Stdout: "[not json"}
	_, err = h.claims.Beat(ctx(), "A")
	require.ErrorContains(t, err, "cannot parse `bd show")

	h.bd.add("p-agent-session-A", fields{"issue_type": agentKind})
	h.bd.fail["show"] = model.CommandResult{Stdout: `{"id":"p-agent-session-A"}`}
	_, err = h.claims.Beat(ctx(), "A")
	require.NoError(t, err)
}

func TestBeat_ACreatedBeadThatCannotBeShownReportsTheShowError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	shows := 0
	h.bd.hooks = append(h.bd.hooks, func(args []string) {
		if args[0] == "show" {
			shows++
			if shows == 2 {
				h.bd.fail["show"] = model.CommandResult{Stdout: "garbage"}
			}
		}
	})

	_, err := h.claims.Beat(ctx(), "A")
	assert.ErrorContains(t, err, "cannot parse")
}

func TestWho_SaysUnownedOneHolderOrMany(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	who, err := h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 unowned", who)

	h.hold("5", sessionA, "")
	who, err = h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 held by session-A", who)

	h.hold("5", sessionB, "-twin")
	h.hold("5", sessionA, "-again")
	who, err = h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 held by session-A, session-B (MORE THAN ONE HOLDER: resolve by hand)", who)
}

func TestWho_IgnoresRowsThatAreNotClaimsOrNotHeld(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.add("x1", fields{"title": "lock:gh-5 t", "status": "open", "assignee": sessionA})
	h.bd.add("x2", fields{"title": "plain work", "status": progress, "assignee": sessionA})
	h.bd.add("x3", fields{"title": "lock:gh-abc t", "status": progress, "assignee": sessionA})
	h.bd.add("x4", fields{"title": "lock:bd-5 t", "status": progress, "assignee": sessionA})
	h.bd.add("x5", fields{"title": "lock:gh-p-1 t", "status": progress, "assignee": sessionA})
	h.bd.add("x6", fields{"title": nil, "status": progress, "assignee": sessionA})
	h.bd.rows["x7"] = fields{"title": "lock:gh-5 t", "status": progress, "id": "", "assignee": "session-Z"}
	h.bd.order = append(h.bd.order, "x7")
	h.bd.add("x8", fields{"title": "lock:gh-5 t", "status": progress})

	who, err := h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 held by ", who)
}

func TestWho_IgnoresAnInProgressRowWithALockTitleOfNoKind(t *testing.T) {
	t.Parallel()

	for _, title := range []string{"lock:x", "lock:", "lock:x t", "lock: t", "lock:ghx t"} {
		h := newHarness(t)
		h.bd.add("x1", fields{"title": title, "status": progress, "assignee": sessionA})

		who, err := h.claims.Who(ctx(), "5")
		require.NoError(t, err)
		assert.Equal(t, "#5 unowned", who, title)
	}
}

func TestWho_RefusesAnItemThatIsNeitherKind(t *testing.T) {
	t.Parallel()

	_, err := newHarness(t).claims.Who(ctx(), "x y")
	assert.ErrorContains(t, err, "neither a forge number nor a beads id")
}

func TestWho_AListThatCannotBeReadIsNoAnswer(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.fail["list"] = model.CommandResult{ExitCode: 1, Stderr: strings.Repeat("boom ", 100)}

	_, err := h.claims.Who(ctx(), "5")
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "claim: cannot read the held rows from `bd list` (rc=1)"), err.Error())
	assert.Len(t, strings.SplitN(err.Error(), "answer. ", 2)[1], 200)

	for name, stdout := range map[string]string{"object": `{"id":"x"}`, "null": "null", "rows_that_are_not_objects": "[1]"} {
		h.bd.fail["list"] = model.CommandResult{Stdout: stdout}
		_, err = h.claims.Who(ctx(), "5")
		assert.ErrorContains(t, err, "refusing to answer", name)
	}
}

func TestWho_AFullPageIsNoAnswerAndOneUnderIsRead(t *testing.T) {
	t.Parallel()

	page := make([]fields, 5000)
	for i := range page {
		page[i] = fields{"id": "r" + strconv.Itoa(i), "title": "x", "status": progress}
	}

	h := newHarness(t)
	h.bd.fail["list"] = model.CommandResult{Stdout: h.bd.json(page)}

	_, err := h.claims.Who(ctx(), "5")
	require.ErrorContains(t, err, "claim: the held rows returned 5000 rows, the list may be cut off; refusing")

	h.bd.fail["list"] = model.CommandResult{Stdout: h.bd.json(page[1:])}

	who, err := h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 unowned", who)
}

func TestWho_AgentBeadsOfOtherOwnersOrWithoutAStampAreNotRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, "")
	h.bd.add("other-agent-session-B", fields{"issue_type": agentKind, "last_activity": "2000-01-01T00:00:00Z"})
	h.bd.add("p-agent-session-B", fields{"issue_type": agentKind, "last_activity": "not a time"})

	who, err := h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 held by session-B", who)

	h.bd.rows["p-agent-session-B"]["last_activity"] = ""
	who, err = h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 held by session-B", who)
}

func TestWho_AnAgentListThatFailsIsNoAnswer(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, "")
	h.bd.hooks = append(h.bd.hooks, func(args []string) {
		if args[0] == "list" && slices.Contains(args, "--type") {
			h.bd.fail["list"] = model.CommandResult{Stdout: "garbage"}
		}
	})

	_, err := h.claims.Who(ctx(), "5")
	assert.ErrorContains(t, err, "the agent beads")
}

func TestRelease_ClosesTheRowAndClearsTheAssignee(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, "")
	require.NoError(t, h.claims.Release(ctx(), "A", "5"))
	assert.Equal(t, "closed", h.bd.rows[rowGH5]["status"])
	assert.Empty(t, h.bd.rows[rowGH5]["assignee"])
	assert.Equal(t, "released #5\n", h.said())

	who, err := h.claims.Who(ctx(), "5")
	require.NoError(t, err)
	assert.Equal(t, "#5 unowned", who)
}

func TestRelease_OfAnItemNobodyHoldsSaysSo(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, h.claims.Release(ctx(), "A", "5"))
	assert.Equal(t, "#5 was not locked\n", h.said())
}

func TestRelease_OfAnotherOwnersHoldRefusesWithCodeThree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionB, "")
	h.hold("5", "session-C", "-twin")
	h.hold("5", sessionB, "-again")

	err := h.claims.Release(ctx(), "A", "5")
	require.EqualError(t, err, "REFUSED: #5 is held by session-B, session-C, not session-A")
	assert.Equal(t, claim.CodeHeld, h.code(err))
	assert.Equal(t, progress, h.bd.rows[rowGH5]["status"])
}

func TestRelease_ThatDidNotTakeEffectIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, "")
	h.bd.fail["update"] = model.CommandResult{}

	err := h.claims.Release(ctx(), "A", "5")
	require.EqualError(t, err, `claim: release of #5 did not take effect on p-lock-gh-5: status "in_progress", assignee "session-A"`)
	assert.Equal(t, claim.CodeRefused, h.code(err))

	closed := newHarness(t)
	closed.hold("5", sessionA, "")
	closed.bd.fail["update"] = model.CommandResult{}
	closed.bd.hooks = append(closed.bd.hooks, func(args []string) {
		if args[0] == "update" {
			closed.bd.rows[rowGH5]["status"], closed.bd.rows[rowGH5]["assignee"] = "closed", sessionB
		}
	})
	assert.ErrorContains(t, closed.claims.Release(ctx(), "A", "5"), `status "closed", assignee "session-B"`)
}

func TestRelease_WhoseRowVanishedIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, "")
	h.bd.hooks = append(h.bd.hooks, func(args []string) {
		if args[0] == "update" {
			delete(h.bd.rows, rowGH5)
		}
	})
	assert.ErrorContains(t, h.claims.Release(ctx(), "A", "5"), "did not take effect on p-lock-gh-5: the row is gone")
}

func TestRelease_ClosesEveryRowTheOwnerHoldsForTheItem(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hold("5", sessionA, "")
	h.hold("5", sessionA, "-twin")
	require.NoError(t, h.claims.Release(ctx(), "A", "5"))
	assert.Equal(t, "released #5\n", h.said())

	for _, r := range h.bd.rows {
		assert.Equal(t, "closed", r["status"])
	}
}

func TestRelease_RefusesBadNamesAndUnreadableRows(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.ErrorContains(t, h.claims.Release(ctx(), "a-b", "5"), "owner token")
	require.ErrorContains(t, h.claims.Release(ctx(), "A", "x y"), "neither a forge number")

	h.bd.fail["list"] = model.CommandResult{Stdout: "garbage"}
	require.ErrorContains(t, h.claims.Release(ctx(), "A", "5"), "refusing to answer")

	broken := newHarness(t)
	broken.hold("5", sessionA, "")
	broken.bd.hooks = append(broken.bd.hooks, func(args []string) {
		if args[0] == "update" {
			broken.bd.fail["show"] = model.CommandResult{Stdout: "garbage"}
		}
	})
	require.ErrorContains(t, broken.claims.Release(ctx(), "A", "5"), "cannot parse")
}

func TestTake_RefusesBadNamesAndUnreadableState(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.ErrorContains(t, h.claims.Take(ctx(), "a-b", "5"), "owner token")
	require.ErrorContains(t, h.claims.Take(ctx(), "A", "x y"), "neither a forge number")

	listFails := newHarness(t)
	listFails.bd.fail["list"] = model.CommandResult{Stdout: "garbage"}
	require.ErrorContains(t, listFails.claims.Take(ctx(), "A", "5"), "refusing to answer")

	// Each stage of a take reads the rows; make the nth list call fail to reach each read.
	for stage := 2; stage <= 5; stage++ {
		staged := newHarness(t)
		staged.hold("5", sessionB, "")
		staged.seen(sessionB, lease+60)

		calls := 0
		staged.bd.hooks = append(staged.bd.hooks, func(args []string) {
			if args[0] == "list" {
				calls++
				if calls == stage {
					staged.bd.fail["list"] = model.CommandResult{Stdout: "garbage"}
				}
			}
		})
		require.ErrorContains(t, staged.claims.Take(ctx(), "A", "5"), "refusing to answer", stage)
	}
}

func TestTake_RefusesWhenARowReadFailsMidway(t *testing.T) {
	t.Parallel()

	for stage := 2; stage <= 6; stage++ {
		staged := newHarness(t)
		staged.hold("5", sessionB, "")
		staged.seen(sessionB, lease+60)

		calls := 0
		staged.bd.hooks = append(staged.bd.hooks, func(args []string) {
			if args[0] == "show" {
				calls++
				if calls == stage {
					staged.bd.fail["show"] = model.CommandResult{Stdout: "garbage"}
				}
			}
		})

		err := staged.claims.Take(ctx(), "A", "5")
		require.ErrorContains(t, err, "cannot parse `bd show", stage)
	}
}

func TestTake_AWarningGoesToTheWarnWriterAndTheClaimToTheOutWriter(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.heartbeatFails = true
	h.bd.fail["agent"] = model.CommandResult{ExitCode: 1, Stderr: "no heartbeat"}
	require.NoError(t, h.claims.Take(ctx(), "A", "5"))
	assert.Equal(t, "TAKEN #5 by session-A\n", h.said())
	assert.True(t, strings.HasPrefix(h.warn.String(), "WARNING: heartbeat for p-agent-session-A failed: no heartbeat."))
}

func TestTake_ABeadsItemWhoseShowIsUnreadableIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.add("p-7", fields{"title": "work"})

	shows := 0
	h.bd.hooks = append(h.bd.hooks, func(args []string) {
		if args[0] == "show" {
			shows++
			if shows == 4 {
				h.bd.fail["show"] = model.CommandResult{Stdout: "garbage"}
			}
		}
	})
	assert.ErrorContains(t, h.claims.Take(ctx(), "A", "p-7"), "cannot parse `bd show p-7`")
}

func TestNew_RefusesWhatWouldLetASecondRunTakeALiveHold(t *testing.T) {
	t.Parallel()

	good := claim.Options{
		BD: newFakeBd(t), GH: &fakeGh{}, Prefix: "p", Repo: "owner/name", Lease: model.DefaultClaimLease, Settle: model.DefaultClaimSettle,
		Clock: time.Now, Sleep: time.Sleep, Out: &bytes.Buffer{}, Warn: &bytes.Buffer{},
	}
	tests := map[string]struct {
		change func(*claim.Options)
		want   string
	}{
		"zero_lease":      {func(o *claim.Options) { o.Lease = 0 }, "the lease must be at least 1s, got 0s"},
		"sub_second":      {func(o *claim.Options) { o.Lease = 999 * time.Millisecond }, "the lease must be at least 1s"},
		"negative_settle": {func(o *claim.Options) { o.Settle = -time.Second }, "the settle pause must not be negative"},
		"no_bd":           {func(o *claim.Options) { o.BD = nil }, "the BD runner is required"},
		"no_gh":           {func(o *claim.Options) { o.GH = nil }, "the GH runner is required"},
		"no_clock":        {func(o *claim.Options) { o.Clock = nil }, "the clock is required"},
		"no_sleep":        {func(o *claim.Options) { o.Sleep = nil }, "the sleep function is required"},
		"no_out":          {func(o *claim.Options) { o.Out = nil }, "the output writer is required"},
		"no_warn":         {func(o *claim.Options) { o.Warn = nil }, "the warning writer is required"},
		"no_prefix":       {func(o *claim.Options) { o.Prefix = "" }, "the id prefix is required"},
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

func TestListRefusals_KeepTheirCause(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.bd.fail["list"] = model.CommandResult{Stdout: "null"}

	_, err := h.claims.Who(ctx(), "5")
	require.ErrorIs(t, err, model.ErrNullAnswer)

	h.bd.fail["list"] = model.CommandResult{Stdout: "nope"}
	_, err = h.claims.Who(ctx(), "5")

	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax)

	h.bd.fail["show"] = model.CommandResult{Stdout: "nope"}
	_, err = h.claims.Beat(ctx(), "A")
	require.ErrorAs(t, err, &syntax)
}
