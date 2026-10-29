package beads_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/beads"
)

const rowA = "p-lock-gh-5"

func TestBeadsTryHold_ABdWithoutTheGuardsIsARefusalAndNothingIsCleared(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.NoGuard = true
	bd.Add(rowA, fields{"title": "lock:gh-5", "status": "closed", "assignee": "session-old"})

	err := beadsOver(t, bd).TryHold(bg(), model.ClaimRow{Item: "5", Holder: "session-b"})

	require.ErrorContains(t, err, "the guards need bd 1.3.2 or newer")
	assert.Equal(t, "session-old", bd.Rows[rowA]["assignee"])
}

func TestRequireBd(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		result model.CommandResult
		want   string
	}{
		"current":               {model.CommandResult{Stdout: "bd version 1.3.2 (dev)\n"}, ""},
		"newer_minor":           {model.CommandResult{Stdout: "bd version 1.10.0 (abc: main@123)\n"}, ""},
		"newer_major":           {model.CommandResult{Stdout: "bd version 2.0.0 (dev)\n"}, ""},
		"older_patch":           {model.CommandResult{Stdout: "bd version 1.3.1 (dev)\n"}, "obee needs bd 1.3.2 or newer for claims; found 1.3.1"},
		"older_major":           {model.CommandResult{Stdout: "bd version 0.56.1 (dev)\n"}, "obee needs bd 1.3.2 or newer for claims; found 0.56.1"},
		"release_cand":          {model.CommandResult{Stdout: "bd version 1.3.2-rc.3 (dev)\n"}, "obee needs bd 1.3.2 or newer for claims; found 1.3.2-rc.3"},
		"huge_major":            {model.CommandResult{Stdout: "bd version 99999999999999999999.0.0 (dev)\n"}, ""},
		"leading_zero":          {model.CommandResult{Stdout: "bd version 1.03.2 (dev)\n"}, `cannot read a version from "bd version 1.03.2 (dev)"`},
		"leading_zero_major":    {model.CommandResult{Stdout: "bd version 01.3.2 (dev)\n"}, `cannot read a version from "bd version 01.3.2 (dev)"`},
		"leading_zero_patch":    {model.CommandResult{Stdout: "bd version 1.3.02 (dev)\n"}, `cannot read a version from "bd version 1.3.02 (dev)"`},
		"huge_minor":            {model.CommandResult{Stdout: "bd version 1.99999999999999999999.0 (dev)\n"}, ""},
		"pre_release_trailing":  {model.CommandResult{Stdout: "bd version 1.4.0-rc. (dev)\n"}, `cannot read a version from "bd version 1.4.0-rc. (dev)"`},
		"pre_release_empty_dot": {model.CommandResult{Stdout: "bd version 1.4.0-.x (dev)\n"}, `cannot read a version from "bd version 1.4.0-.x (dev)"`},
		"pre_release_two_dots":  {model.CommandResult{Stdout: "bd version 1.4.0-rc..1 (dev)\n"}, `cannot read a version from "bd version 1.4.0-rc..1 (dev)"`},
		"pre_release_zero_led":  {model.CommandResult{Stdout: "bd version 1.4.0-01 (dev)\n"}, `cannot read a version from "bd version 1.4.0-01 (dev)"`},
		"pre_release_zeros":     {model.CommandResult{Stdout: "bd version 1.3.3-00 (dev)\n"}, `cannot read a version from "bd version 1.3.3-00 (dev)"`},
		"unreadable":            {model.CommandResult{Stdout: "hello\n"}, `cannot read a version from "hello"`},
		"command_failed":        {model.CommandResult{ExitCode: 127, Stderr: "bd: not found"}, "cannot run `bd --version`: bd: not found"},
	}

	for name, tc := range tests {
		bd := newFakeBd(t)
		bd.Fail["--version"] = tc.result

		err := beads.RequireBd(bg(), bd)

		if tc.want == "" {
			require.NoError(t, err, name)

			continue
		}

		require.ErrorContains(t, err, tc.want, name)
		assert.Equal(t, store.CodeRefused, codeOf(t, err), name)
	}
}

func TestBeadsClaimWrites_RefuseAnOldBdBeforeTheyWrite(t *testing.T) {
	t.Parallel()

	bd := newFakeBd(t)
	bd.Version = "0.56.1"
	bd.Add(rowA, fields{"title": "lock:gh-5", "status": progress, "assignee": "session-a"})
	beads := beadsOver(t, bd)
	row := model.ClaimRow{Item: "5", Holder: "session-a"}
	_, reclaimErr := beads.Reclaim(bg(), "5")

	for name, err := range map[string]error{"take": beads.TryHold(bg(), row), "beat": beads.Beat(bg(), "session-a"), "reclaim": reclaimErr, "release": beads.Free(bg(), row)} {
		require.ErrorContains(t, err, "obee needs bd 1.3.2 or newer for claims; found 0.56.1", name)
	}

	assert.Equal(t, 0, bd.Count("update")+bd.Count("unclaim")+bd.Count("create")+bd.Count("heartbeat")+bd.Count("reclaim"), "an old bd is never written to")

	bd.Version = "1.3.2"
	require.NoError(t, beads.Beat(bg(), "session-a"), "the gate asks again after a refusal")

	count := bd.Count("--version")
	require.NoError(t, beads.Beat(bg(), "session-a"))
	assert.Equal(t, count, bd.Count("--version"), "a bd that passed is not asked again")
}
