package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/landing"
)

// landingProofCall writes a call of landing_proof over the landed members of the items, from the tip t0 to the tip t1, and a report of the unproven list.
func (r *batchRig) landingProofCall(items ...string) string {
	landed := make([]string, 0, len(items))
	for _, item := range items {
		landed = append(landed, `struct(item = "`+item+`", head = "`+r.heads[item]+`")`)
	}

	return `t0 = struct(commit = "c0", tree = "t0")
t1 = struct(commit = "c1", tree = "t1")
p = landing_proof(base = "main", start = t0, end = t1, landed = [` + strings.Join(landed, ", ") + `], name = "p")
command(name = "report", run = ["report", ",".join([u.item + ":" + u.reason for u in p.unproven])])
`
}

func TestLandingProof_ListsTheMembersItCannotFindOnTheBase(t *testing.T) {
	t.Parallel()

	t.Run("lists_the_unproven_members", func(t *testing.T) {
		t.Parallel()

		rig := newBatchRig(t)
		rig.host.unproven = []landing.Outcome{{Item: "3", Reason: "its head is not on main"}}

		_, err := rig.walk(t, rig.landingProofCall("1", "3"))

		require.NoError(t, err)
		assert.Equal(t, []string{"report 3:its head is not on main"}, filterReport(rig.h.commands.ran()))
		assert.Equal(t, []string{testProject + " main c0>c1 1=" + rig.heads["1"] + ",3=" + rig.heads["3"]}, rig.host.proofReads(), "one read over every landed member")
	})

	t.Run("an_empty_landed_list_reads_nothing", func(t *testing.T) {
		t.Parallel()

		rig := newBatchRig(t)

		_, err := rig.walk(t, rig.landingProofCall())

		require.NoError(t, err)
		assert.Equal(t, []string{"report "}, filterReport(rig.h.commands.ran()))
		assert.Empty(t, rig.host.proofReads())
	})

	t.Run("a_failed_call_ends_the_step", func(t *testing.T) {
		t.Parallel()

		rig := newBatchRig(t)
		rig.host.proofErr = errFake

		_, err := rig.walk(t, rig.landingProofCall("1"))

		require.ErrorIs(t, err, errFake)
		assert.Empty(t, filterReport(rig.h.commands.ran()))
	})

	t.Run("no_forge_fails_with_the_absent_error", func(t *testing.T) {
		t.Parallel()

		rig := newBatchRig(t)
		deps := rig.deps()
		deps.Batch = nil

		engine, err := NewEngine(t.Context(), rig.h.opts, deps)
		require.NoError(t, err)
		require.NoError(t, engine.Launch())
		t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

		_, err = engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.landingProofCall("1"))), modeOff())

		require.ErrorContains(t, err, "this build cannot prove a landing")
	})
}

func TestLandingProof_AWrongTipOrMemberFailsBeforeAnyRead(t *testing.T) {
	t.Parallel()

	tip := `struct(commit = "c", tree = "t")`
	cases := map[string]struct{ start, end, landed, want string }{
		"start_not_a_struct":    {`"c"`, tip, `[]`, "`start` must be a struct with the field commit, not string"},
		"start_without_tree":    {`struct(commit = "c")`, tip, `[]`, "`start` needs the field tree as text that is not empty"},
		"end_with_empty_commit": {tip, `struct(commit = "", tree = "t")`, `[]`, "`end` needs the field commit as text that is not empty"},
		"landed_not_a_list":     {tip, tip, `len([])`, "`landed` must be a list of struct(item = ..., head = ...), not int"},
		"member_without_head":   {tip, tip, `[struct(item = "1")]`, "`landed[0]` needs the field head as text that is not empty"},
		"member_without_item":   {tip, tip, `[struct(head = "h")]`, "`landed[0]` needs the field item as text that is not empty"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)
			call := "landing_proof(base = \"main\", start = " + c.start + ", end = " + c.end + ", landed = " + c.landed + ", name = \"p\")\n"

			_, err := rig.walk(t, call)

			require.ErrorContains(t, err, "landing_proof: "+c.want)
			assert.Empty(t, rig.host.proofReads())
		})
	}
}

func TestLandingProof_ANameThatTheRunComputesAsANumberIsRefused(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, `landing_proof(base = "main", start = None, end = None, landed = [], name = len([]))
`)

	require.ErrorContains(t, err, `landing_proof: for parameter "name": got int, want string`)
	assert.Empty(t, rig.host.proofReads())
}

func TestLandingProof_TheResultIsReadFromTheRecordOfTheStep(t *testing.T) {
	t.Parallel()

	out, err := proofResult(landingProofOutputs([]landing.Outcome{{Item: "3", Reason: "r"}}))
	require.NoError(t, err)
	assert.Equal(t, `struct(unproven = [struct(item = "3", reason = "r")])`, out.String())

	_, err = proofResult(map[string]any{unprovenField: "not a list"})
	require.ErrorContains(t, err, "read the record of landing_proof")

	_, err = proofResult(map[string]any{unprovenField: func() {}})
	require.ErrorContains(t, err, "read the record of landing_proof")
}
