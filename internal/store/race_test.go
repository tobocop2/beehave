package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
)

const (
	rowA  = "p-lock-gh-5"
	oldA  = "session-a#TOKEN1"
	tokA2 = "session-a#TOKEN2"
)

func TestBeadsFree_MissingRowIsFreeAndAnotherTakeStays(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	beads := beadsOver(t, bd)
	require.NoError(t, beads.Free(bg(), model.ClaimRow{Item: "5", Holder: "session-a", Nonce: "TOKEN1"}), "a missing row is free")

	bd.Add(rowA, fields{"title": "lock:gh-5", "status": progress, "assignee": "session-b#X"})
	require.ErrorIs(t, beads.Free(bg(), model.ClaimRow{Item: "5", Holder: "session-a", Nonce: "TOKEN1"}), model.ErrRowMoved)
	assert.Equal(t, "session-b#X", bd.Rows[rowA]["assignee"])

	bd.Fail["unclaim"] = model.CommandResult{ExitCode: 1, Stderr: "locked"}
	require.ErrorContains(t, beads.Free(bg(), model.ClaimRow{Item: "5", Holder: "session-b", Nonce: "X"}), "locked")

	bd.Fail["show"] = model.CommandResult{ExitCode: 1, Stderr: "disk error"}
	require.ErrorContains(t, beads.Free(bg(), model.ClaimRow{Item: "5", Holder: "session-b", Nonce: "X"}), "disk error")
}

func TestBeadsFree_OfARowThatWasFreedMeanwhileIsFree(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.Add(rowA, fields{"title": "lock:gh-5", "status": "closed"})

	require.NoError(t, beadsOver(t, bd).Free(bg(), model.ClaimRow{Item: "5", Holder: "session-a", Nonce: "TOKEN1"}))
}

func TestBeadsStoreLease_RefusesATakeThatChangedHands(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.Add(rowA, fields{"title": "lock:gh-5", "status": progress, "assignee": tokA2, "description": "lease=99"})

	err := beadsOver(t, bd).StoreLease(bg(), model.ClaimRow{Item: "5", Holder: "session-a", Nonce: "TOKEN1", Lease: 60})

	require.ErrorIs(t, err, model.ErrRowMoved)
	assert.Equal(t, "lease=99", bd.Rows[rowA]["description"])
}

func TestBeadsGuards_NameTheTakeAndTheStatus(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	beads := beadsOver(t, bd)
	row := model.ClaimRow{Item: "5", Holder: "session-a", Nonce: "TOKEN1", Lease: 60}
	require.NoError(t, beads.TryHold(bg(), row))
	require.NoError(t, beads.StoreLease(bg(), row))
	require.NoError(t, beads.Free(bg(), row))

	assert.True(t, bd.Called("update", rowA, "--claim", "--assignee", oldA))
	assert.True(t, bd.Called("update", rowA, "--if-assignee", oldA, "--if-status", progress, "--description",
		"Claim for 5. Held while in_progress; the assignee holds it. lease=60"))
	assert.True(t, bd.Called("unclaim", rowA, "--if-assignee", oldA))
}

func TestBeadsRows_ShowTheHolderWithoutTheToken(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.Add(rowA, fields{"title": "lock:gh-5", "status": progress, "assignee": oldA, "description": "lease=60"})
	beads := beadsOver(t, bd)

	state, err := beads.Row(bg(), "5")
	require.NoError(t, err)
	assert.Equal(t, model.RowState{Exists: true, Held: true, Holder: "session-a", Nonce: "TOKEN1"}, state)

	held, err := beads.HeldRows(bg())
	require.NoError(t, err)
	assert.Equal(t, []model.ClaimRow{{Item: "5", Holder: "session-a", Lease: 60, Nonce: "TOKEN1"}}, held)
}

func TestBeadsMissingRowText_BothVersionsOfBdAreRecognised(t *testing.T) {
	t.Parallel()

	for name, v1 := range map[string]bool{"v0.56.1": false, "v1.3.2": true} {
		bd := newFakeBd(t)
		bd.V1 = v1

		state, err := beadsOver(t, bd).Row(bg(), "5")

		require.NoError(t, err, name)
		assert.Equal(t, model.RowState{}, state, name)
	}

	bd := newFakeBd(t)
	bd.Fail["show"] = model.CommandResult{ExitCode: 1, Stderr: "no issues found matching the provided IDs"}
	state, err := beadsOver(t, bd).Row(bg(), "5")
	require.NoError(t, err)
	assert.Equal(t, model.RowState{}, state)
}

func TestBeadsTryHold_ABdWithoutTheGuardsIsARefusalAndNothingIsCleared(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.NoGuard = true
	bd.Add(rowA, fields{"title": "lock:gh-5", "status": "closed", "assignee": "session-old#Z"})

	err := beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "session-b", Nonce: "T"})

	require.ErrorContains(t, err, "the guards need bd 1.3.2 or newer")
	assert.Equal(t, "session-old#Z", bd.Rows[rowA]["assignee"])
}

func TestRequireBd(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		result model.CommandResult
		want   string
	}{
		"current":        {model.CommandResult{Stdout: "bd version 1.3.2 (dev)\n"}, ""},
		"newer_minor":    {model.CommandResult{Stdout: "bd version 1.10.0 (abc: main@123)\n"}, ""},
		"newer_major":    {model.CommandResult{Stdout: "bd version 2.0.0 (dev)\n"}, ""},
		"older_patch":    {model.CommandResult{Stdout: "bd version 1.3.1 (dev)\n"}, "obee needs bd 1.3.2 or newer for claims; found 1.3.1"},
		"older_major":    {model.CommandResult{Stdout: "bd version 0.56.1 (dev)\n"}, "obee needs bd 1.3.2 or newer for claims; found 0.56.1"},
		"release_cand":   {model.CommandResult{Stdout: "bd version 1.3.2-rc.3 (dev)\n"}, "obee needs bd 1.3.2 or newer for claims; found 1.3.2-rc.3"},
		"too_large":      {model.CommandResult{Stdout: "bd version 99999999999999999999.0.0 (dev)\n"}, "cannot read a version from"},
		"unreadable":     {model.CommandResult{Stdout: "hello\n"}, `cannot read a version from "hello"`},
		"command_failed": {model.CommandResult{ExitCode: 127, Stderr: "bd: not found"}, "cannot run `bd --version`: bd: not found"},
	}

	for name, tc := range tests {
		bd := newFakeBd(t)
		bd.Fail["--version"] = tc.result

		err := store.RequireBd(bg(), bd)

		if tc.want == "" {
			require.NoError(t, err, name)

			continue
		}

		require.ErrorContains(t, err, tc.want, name)
		assert.Equal(t, store.CodeRefused, codeOf(t, err), name)
	}
}

func TestBeadsLeaseWrites_RefuseAnOldBdBeforeTheyWrite(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.Version = "0.56.1"
	bd.Add(rowA, fields{"title": "lock:gh-5", "status": progress, "assignee": oldA})
	beads := beadsOver(t, bd)
	row := model.ClaimRow{Item: "5", Holder: "session-a", Nonce: "TOKEN1", Lease: 60}

	for name, err := range map[string]error{"take": beads.TryHold(bg(), row), "lease": beads.StoreLease(bg(), row), "release": beads.Free(bg(), row)} {
		require.ErrorContains(t, err, "obee needs bd 1.3.2 or newer for claims; found 0.56.1", name)
	}

	assert.Equal(t, 0, bd.Count("update")+bd.Count("unclaim")+bd.Count("create"), "an old bd is never written to")

	bd.Version = "1.3.2"
	require.NoError(t, beads.StoreLease(bg(), row), "the gate asks again after a refusal")

	count := bd.Count("--version")
	require.NoError(t, beads.StoreLease(bg(), row))
	assert.Equal(t, count, bd.Count("--version"), "a bd that passed is not asked again")
}
