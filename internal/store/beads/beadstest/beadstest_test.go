package beadstest_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
)

func run(runner proc.Runner, args ...string) model.CommandResult {
	return runner.Run(context.Background(), args)
}

func TestBlocks_HoldTheBeadsBlockUnderItsName(t *testing.T) {
	t.Parallel()

	prefix, given := beadstest.Blocks("p")["beads"].Text("prefix")

	assert.True(t, given)
	assert.Equal(t, "p", prefix)
}

func TestBlock_HoldsTheIDPrefixUnderTheKeyOfTheBeadsBlock(t *testing.T) {
	t.Parallel()

	prefix, given := beadstest.Block("p").Text("prefix")

	assert.True(t, given)
	assert.Equal(t, "p", prefix)
}

func TestBd_ListFiltersByStatusAndType(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Add("p-1", beadstest.Fields{"status": beadstest.Progress, "issue_type": "task"})
	bd.Add("p-2", beadstest.Fields{"issue_type": "agent"})
	bd.Add("p-1", beadstest.Fields{"status": beadstest.Progress, "issue_type": "task", "title": "again"})

	var rows []map[string]any

	require.NoError(t, json.Unmarshal([]byte(run(bd, "list").Stdout), &rows))
	assert.Len(t, rows, 2)

	require.NoError(t, json.Unmarshal([]byte(run(bd, "list", "--status="+beadstest.Progress).Stdout), &rows))
	assert.Len(t, rows, 1)

	require.NoError(t, json.Unmarshal([]byte(run(bd, "list", "--type", "agent").Stdout), &rows))
	assert.Equal(t, "p-2", rows[0]["id"])
	assert.Equal(t, 3, bd.Count("list"))
	assert.True(t, bd.Called("list", "--type", "agent"))
	assert.False(t, bd.Called("list", "--type"))
}

func TestBd_ShowResolvesAPartialIdAndPrintsTheTextOfTheVersion(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Add("p-lock-gh-5", nil)
	bd.Add("p-lock-gh-6", nil)

	assert.Equal(t, 0, run(bd, "show", "p-lock-gh-5").ExitCode)
	assert.Equal(t, 0, run(bd, "show", "p-lock-gh-5", "--json").ExitCode)

	ambiguous := run(bd, "show", "p-lock")
	assert.Equal(t, 1, ambiguous.ExitCode)
	assert.Equal(t, `Error fetching p-lock: no issue found matching "p-lock"`, ambiguous.Stderr)

	bd.V1 = true
	assert.Equal(t, "Issue p-x not found\nHint: check the id", run(bd, "show", "p-x").Stderr)

	bd.Add("p-lock-gh-7-extra", nil)
	assert.Equal(t, 0, run(bd, "show", "p-lock-gh-7").ExitCode, "one row starts with the id")
}

func TestBd_CreateAndUpdate(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	assert.Equal(t, "p-1", run(bd, "create", "--id", "p-1", "--title", "t", "--description", "d").Stdout)
	assert.Equal(t, "task", bd.Rows["p-1"]["issue_type"])
	run(bd, "create", "--id", "p-2", "--type", "agent")
	assert.Equal(t, "agent", bd.Rows["p-2"]["issue_type"])

	missing := run(bd, "update", "p-9", "--status", "closed")
	assert.Equal(t, 1, missing.ExitCode)
	assert.Equal(t, `Error resolving p-9: no issue found matching "p-9"`, missing.Stderr)

	assert.Equal(t, 0, run(bd, "update", "p-1", "--description", "new", "--status", "closed").ExitCode)
	assert.Equal(t, "new", bd.Rows["p-1"]["description"])
	assert.Equal(t, "closed", bd.Rows["p-1"]["status"])
}

func TestBd_ClaimFollowsBd132(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Add("p-1", nil)

	assert.Equal(t, 0, run(bd, "update", "p-1", "--claim", "--actor", "a").ExitCode)
	assert.Equal(t, beadstest.Progress, bd.Rows["p-1"]["status"])
	assert.Equal(t, "a", bd.Rows["p-1"]["assignee"])
	assert.Equal(t, beadstest.At(beadstest.Now+beadstest.LeaseSeconds), bd.Rows["p-1"]["lease_expires_at"], "the claim grants the lease to the actor")
	assert.Equal(t, 0, run(bd, "update", "p-1", "--claim", "--actor", "a").ExitCode, "a claim by the holder changes nothing")

	lost := run(bd, "update", "p-1", "--claim", "--actor", "b")
	assert.Equal(t, 1, lost.ExitCode, "bd 1.3.2 exits 1 on a refused claim")
	assert.Contains(t, lost.Stderr, "issue already claimed by a")
	assert.Equal(t, "a", bd.Rows["p-1"]["assignee"])

	bd.Add("p-2", beadstest.Fields{"assignee": "old"})
	assigned := run(bd, "update", "p-2", "--claim", "--actor", "b")
	assert.Equal(t, 1, assigned.ExitCode)
	assert.Contains(t, assigned.Stderr, `issue already claimed: already assigned to "old"`)

	bd.Add("p-3", beadstest.Fields{"status": "closed"})
	closed := run(bd, "update", "p-3", "--claim", "--actor", "b")
	assert.Equal(t, 1, closed.ExitCode)
	assert.Contains(t, closed.Stderr, "issue not claimable: status closed")
	assert.Equal(t, "closed", bd.Rows["p-3"]["status"], "a claim keeps the status filter")
}

func TestBd_UnclaimReopensTheRowOnlyForTheNamedAssignee(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Add("p-1", beadstest.Fields{"status": beadstest.Progress, "assignee": "a#1"})

	mismatch := run(bd, "unclaim", "p-1", "--if-assignee", "a#2")
	assert.Equal(t, 1, mismatch.ExitCode, "bd 1.3.2 exits 1 on a release mismatch")
	assert.Contains(t, mismatch.Stderr, `assignee mismatch: p-1 is held by "a#1", expected "a#2"`)
	assert.Equal(t, "a#1", bd.Rows["p-1"]["assignee"])

	assert.Equal(t, 0, run(bd, "unclaim", "p-1", "--if-assignee", "a#1").ExitCode)
	assert.Equal(t, "open", bd.Rows["p-1"]["status"])
	assert.Empty(t, bd.Rows["p-1"]["assignee"])

	assert.Contains(t, run(bd, "unclaim", "p-1", "--if-assignee", "a#1").Stderr, "assignee mismatch")

	bd.Rows["p-1"]["status"] = "closed"
	assert.Contains(t, run(bd, "unclaim", "p-1", "--if-assignee", "").Stderr, "cannot unclaim closed issue")
	assert.Equal(t, 1, run(bd, "unclaim", "p-9", "--if-assignee", "a").ExitCode)

	bd.NoGuard = true
	assert.Contains(t, run(bd, "unclaim", "p-1", "--if-assignee", "a").Stderr, "unknown flag")
}

func TestBd_GuardWritesNothingWhenTheAssigneeDiffers(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Add("p-1", beadstest.Fields{"assignee": "a"})

	mismatch := run(bd, "update", "p-1", "--if-assignee", "b", "--assignee", "")
	assert.Equal(t, 13, mismatch.ExitCode)
	assert.Equal(t, "a", bd.Rows["p-1"]["assignee"])

	assert.Equal(t, 0, run(bd, "update", "p-1", "--if-assignee", "a", "--assignee", "").ExitCode)
	assert.Empty(t, bd.Rows["p-1"]["assignee"])

	bd.Rows["p-1"]["status"] = "closed"
	status := run(bd, "update", "p-1", "--if-assignee", "", "--if-status", "in_progress", "--assignee", "x")
	assert.Equal(t, 13, status.ExitCode)
	assert.Contains(t, status.Stderr, "status mismatch")
	assert.Equal(t, 0, run(bd, "update", "p-1", "--if-status", "closed", "--assignee", "x").ExitCode)
	assert.Equal(t, "x", bd.Rows["p-1"]["assignee"])

	bd.NoGuard = true
	unknown := run(bd, "update", "p-1", "--if-assignee", "", "--assignee", "x")
	assert.Equal(t, 1, unknown.ExitCode)
	assert.Contains(t, unknown.Stderr, "unknown flag")
}

func TestBd_HeartbeatRenewsTheLeaseOfTheHolderOnly(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Add("p-1", beadstest.Fields{"status": beadstest.Progress, "assignee": "a", "lease_expires_at": beadstest.At(beadstest.Now - 10)})

	other := run(bd, "heartbeat", "p-1", "--actor", "b")
	assert.Equal(t, 1, other.ExitCode)
	assert.Contains(t, other.Stderr, "issue already claimed by a")
	assert.Equal(t, beadstest.At(beadstest.Now-10), bd.Rows["p-1"]["lease_expires_at"])

	assert.Equal(t, 0, run(bd, "heartbeat", "p-1", "--actor", "a").ExitCode)
	assert.Equal(t, beadstest.At(beadstest.Now+beadstest.LeaseSeconds), bd.Rows["p-1"]["lease_expires_at"], "a late heartbeat revives the lease before bd reclaim runs")

	gone := run(bd, "heartbeat", "p-9", "--actor", "a")
	assert.Equal(t, 1, gone.ExitCode)
	assert.Equal(t, `Error: resolving p-9: no issue found matching "p-9"`, gone.Stderr)

	bd.Add("p-2", beadstest.Fields{"status": "open"})

	open := run(bd, "heartbeat", "p-2", "--actor", "a")
	assert.Equal(t, 1, open.ExitCode)
	assert.Equal(t, "Error: heartbeat p-2: issue not claimable: p-2 status open", open.Stderr)
}

func TestBd_ReclaimFreesOnlyRowsWhoseLeaseEnded(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Add("p-1", beadstest.Fields{"status": beadstest.Progress, "assignee": "a", "lease_expires_at": beadstest.At(beadstest.Now - 1)})
	bd.Add("p-2", beadstest.Fields{"status": beadstest.Progress, "assignee": "b", "lease_expires_at": beadstest.At(beadstest.Now - 1)})
	bd.Add("p-3", beadstest.Fields{"status": beadstest.Progress, "assignee": "c", "lease_expires_at": beadstest.At(beadstest.Now + 5)})
	bd.Add("p-4", beadstest.Fields{"status": beadstest.Progress, "assignee": "d"})

	var answer struct {
		Count     int `json:"count"`
		Reclaimed []struct {
			ID    string `json:"id"`
			Owner string `json:"previous_owner"`
		} `json:"reclaimed"`
	}

	scoped := run(bd, "reclaim", "--id", "p-1", "--older-than", "0s", "--json")
	require.NoError(t, json.Unmarshal([]byte(scoped.Stdout), &answer))
	assert.Equal(t, 1, answer.Count)
	assert.Equal(t, "p-1", answer.Reclaimed[0].ID)
	assert.Equal(t, "a", answer.Reclaimed[0].Owner)
	assert.Equal(t, "open", bd.Rows["p-1"]["status"])
	assert.Equal(t, beadstest.Progress, bd.Rows["p-2"]["status"], "--id scopes the reclaim")

	run(bd, "reclaim", "--older-than", "0s", "--json")
	assert.Equal(t, "open", bd.Rows["p-2"]["status"])
	assert.Equal(t, beadstest.Progress, bd.Rows["p-3"]["status"], "a live lease stays")
	assert.Equal(t, beadstest.Progress, bd.Rows["p-4"]["status"], "a row with no lease is never reclaimed")
}

func TestBd_HooksAndPlannedFailures(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Add("p-1", nil)

	var seen []string

	bd.Hooks = append(bd.Hooks, func(args []string) { seen = append(seen, args[0]) })

	bd.Fail["show"] = model.CommandResult{ExitCode: 7}
	assert.Equal(t, 7, run(bd, "show", "p-1").ExitCode)

	assert.Equal(t, "fake bd: no answer planned", run(bd, "frobnicate").Stderr)
	assert.Equal(t, []string{"show", "frobnicate"}, seen)
}

func TestOptionsReadsFlagPairs(t *testing.T) {
	t.Parallel()

	assert.Equal(t, map[string]string{"--a": "1", "--b": "--c"}, beadstest.Options([]string{"x", "--a", "1", "--b", "--c"}, 1))
	assert.Empty(t, beadstest.Options([]string{"x"}, 0))
}

func TestJSON_EncodesAValueAndNamesOneItCannotEncode(t *testing.T) {
	t.Parallel()

	assert.JSONEq(t, `{"a":1}`, beadstest.JSON(map[string]int{"a": 1}))
	assert.Contains(t, beadstest.JSON(make(chan int)), "beadstest: json: unsupported type")
}

func TestHookFiresOnceBeforeTheFirstMatchingCommand(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	fired := 0
	hook := &beadstest.Hook{Inner: bd, When: func(args []string) bool { return args[0] == "list" }, Fire: func() { fired++ }}

	run(hook, "show", "p-1")
	assert.Zero(t, fired)

	run(hook, "list")
	run(hook, "list")
	assert.Equal(t, 1, fired)
	assert.Len(t, bd.Calls, 3)
}

func TestReadsFailsReadsAndNotWritesWhileDown(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Add("p-1", nil)
	reads := &beadstest.Reads{Inner: bd}

	assert.Equal(t, 0, run(reads, "show", "p-1").ExitCode)
	reads.Down = true
	assert.Equal(t, 1, run(reads, "list").ExitCode)
	assert.Equal(t, 1, run(reads, "show", "p-1").ExitCode)
	assert.Equal(t, 0, run(reads, "update", "p-1", "--status", "open").ExitCode)
}

func TestBd_VersionAnswersTheVersionOfTheFake(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	assert.Equal(t, "bd version 1.3.2 (dev)\n", run(bd, "--version").Stdout)

	bd.Version = "0.56.1"
	assert.Equal(t, "bd version 0.56.1 (dev)\n", run(bd, "--version").Stdout)
}
