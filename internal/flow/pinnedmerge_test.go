package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinned writes a call of merge with `member` after a gate call that records the step gate:g#1. extra holds more arguments, each followed by a comma,
// and gateArg replaces the gate argument. The report command shows the three answers of the call.
func (r *batchRig) pinned(item, gateArg, extra string) string {
	return `g = gate(name = "g")
out = merge(member = struct(item = "` + item + `", head = "` + r.heads[item] + `", closes = []), base = "main", repo = "o/r", gate = ` + gateArg + `, ` + extra + `name = "m")
command(name = "report", run = ["report", str(out.merged), out.status, out.reason])
`
}

// mainTree is the tree of the tip of main, which is the tree that the test gate reports.
func (r *batchRig) mainTree(t *testing.T) string {
	t.Helper()

	return r.repo.must(t, "rev-parse", "main^{tree}")
}

func TestMerge_AMemberWithAPassedGateOfThisRunMergesAndGetsItsStatuses(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	tree := rig.mainTree(t)

	_, err := rig.walk(t, rig.pinned("1", "g", `gated_tree = g.tree, expect_tree = "`+tree+`", `))

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Equal(t, []string{"beehave/gate success " + rig.heads["1"]}, rig.merger.statusesGiven())
	require.Len(t, rig.merger.given, 1)
	assert.Equal(t, tree, rig.merger.given[0].GatedTree, "the merge compares the base tree with the tree of the gate")
	assert.Equal(t, []string{"report True merged "}, filterReport(rig.h.commands.ran()))
}

func TestMerge_AnApprovedMemberAlsoGetsTheReviewStatus(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	body := `g = gate(name = "g")
out = merge(member = struct(item = "1", head = "` + rig.heads["1"] + `", approved = True, closes = [7]), base = "main", repo = "o/r", gate = g, gated_tree = g.tree, name = "m")
`

	_, err := rig.walk(t, body)

	require.NoError(t, err)
	assert.Equal(t, []string{"beehave/review success " + rig.heads["1"], "beehave/gate success " + rig.heads["1"]}, rig.merger.statusesGiven())
	assert.Equal(t, []int{7}, rig.merger.given[0].Closes)
}

func TestMerge_AStatusIsPostedOnlyForAPassedGateOfThisRun(t *testing.T) {
	t.Parallel()

	madeUp := func(rig *batchRig) string {
		return `struct(step = "gate:g#1", passed = True, head = "h", tree = "` + rig.mainTree(t) + `", steps = [{"name": "command", "status": "pass"}])`
	}

	cases := map[string]struct {
		body func(rig *batchRig) string
		want string
	}{
		"a value that no gate call made": {
			body: func(rig *batchRig) string {
				return `out = merge(member = struct(item = "1", head = "` + rig.heads["1"] + `", closes = []), base = "main", repo = "o/r", gate = ` + madeUp(rig) + `, gated_tree = "` + rig.mainTree(t) + `", name = "m")`
			},
			want: "no gate call of this run recorded it",
		},
		"a gate call that comes after the merge": {
			body: func(rig *batchRig) string {
				return `out = merge(member = struct(item = "1", head = "` + rig.heads["1"] + `", closes = []), base = "main", repo = "o/r", gate = ` + madeUp(rig) + `, gated_tree = "` + rig.mainTree(t) + `", name = "m")
g = gate(name = "g")`
			},
			want: "no gate call of this run recorded it",
		},
		"a value with no step": {
			body: func(rig *batchRig) string {
				return `out = merge(member = struct(item = "1", head = "` + rig.heads["1"] + `", closes = []), base = "main", repo = "o/r", gate = struct(passed = True), gated_tree = "t", name = "m")`
			},
			want: "is not the result of a gate call",
		},
		"a value that is not a struct": {
			body: func(rig *batchRig) string {
				return `out = merge(member = struct(item = "1", head = "` + rig.heads["1"] + `", closes = []), base = "main", repo = "o/r", gate = "gate:g#1", gated_tree = "t", name = "m")`
			},
			want: "`gate` must be the result of a gate call, not string",
		},
		"a gate of this run that ran on another tree than gated_tree": {
			body: func(rig *batchRig) string {
				return rig.pinned("1", "g", `gated_tree = "`+strings.Repeat("0", 40)+`", `)
			},
			want: "gated_tree is",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)

			_, err := rig.walk(t, c.body(rig))

			require.ErrorContains(t, err, c.want)
			assert.Empty(t, rig.merger.statusesGiven(), "no status is posted")
			assert.Empty(t, rig.merger.landed(), "nothing merges")
			assert.Empty(t, rig.host.intents, "the call ends before any read of the pull request")
		})
	}
}

func TestMerge_AFailedGateIsNotTrustedEvenWhenTheValueClaimsAPass(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.repo.must(t, "switch", "-q", "m2")

	// The value copies the step of the real gate, which failed, and claims a pass. The merge reads the record of the run and not the value.
	forged := `struct(step = g.step, passed = True, head = g.head, tree = g.tree, steps = [{"name": "command", "status": "pass"}])`
	body := `g = gate(name = "g")
out = merge(member = struct(item = "1", head = "` + rig.heads["1"] + `", closes = []), base = "main", repo = "o/r", gate = ` + forged + `, gated_tree = g.tree, name = "m")
`

	_, err := rig.walk(t, body)

	require.ErrorContains(t, err, "did not pass, so the merge posts no status and merges nothing")
	assert.Empty(t, rig.merger.statusesGiven())
	assert.Empty(t, rig.merger.landed())
}

func TestMerge_AGateWithOneStepThatIsNotAPassIsNotTrusted(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.gate.unknownStep = true

	_, err := rig.walk(t, rig.pinned("1", "g", `gated_tree = g.tree, `))

	require.ErrorContains(t, err, "did not pass")
	assert.Empty(t, rig.merger.statusesGiven())
	assert.Empty(t, rig.merger.landed())
}

func TestMerge_AReplayOfTheRunTrustsTheGateThatTheRecordHolds(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.tipErrs = []error{errFake, errFake, errFake}
	file := writeFlow(t, bodyFlow(rig.pinned("1", "g", `gated_tree = g.tree, `)))
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file, modeOff())

	require.ErrorIs(t, err, errFake, "the tip read of the intent step fails after the gate step was recorded")
	assert.Empty(t, rig.merger.landed())

	_, err = resumeAndJoin(t, engine, rig.h.client(t, testVersion))

	require.NoError(t, err)
	assert.Len(t, rig.gate.ran(), 1, "the replay reads the recorded gate and runs no gate again")
	assert.Equal(t, []string{"beehave/gate success " + rig.heads["1"]}, rig.merger.statusesGiven(), "the gate of the record is trusted")
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestMerge_ATipWithAnotherTreeThanExpectTreeIsUnsettledAndNothingMerges(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, rig.pinned("1", "g", `gated_tree = g.tree, expect_tree = "`+strings.Repeat("1", 40)+`", `))

	require.NoError(t, err)
	assert.Empty(t, rig.merger.landed(), "no merge")
	assert.Empty(t, rig.merger.statusesGiven(), "no status")
	assert.Empty(t, rig.host.intents, "the pull request is not read for a tip that moved")
	assert.Equal(t, []string{"report False unsettled the base moved while the batch ran; run the batch again"}, filterReport(rig.h.commands.ran()))
}

func TestMerge_ACallWithNoGateMergesWithNoStatusAndNoTreeProof(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	body := `out = merge(member = struct(item = "1", head = "` + rig.heads["1"] + `", closes = []), base = "main", repo = "o/r", expect_tree = "` + rig.mainTree(t) + `", name = "m")
`

	_, err := rig.walk(t, body)

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Empty(t, rig.merger.statusesGiven())
	assert.Empty(t, rig.merger.given[0].GatedTree)
}

func TestMerge_TheResultOfOneMergeHoldsTheTipAfterTheCall(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	body := rig.pinned("1", "g", `gated_tree = g.tree, `) + `command(name = "tip", run = ["tip", out.commit, out.tree])
`

	_, err := rig.walk(t, body)

	require.NoError(t, err)

	want := "tip " + rig.repo.must(t, "rev-parse", "main") + " " + rig.mainTree(t)

	assert.Contains(t, rig.h.commands.ran(), want, "the tip is read after the merge")
}

func TestMerge_ARefusalOfOneMemberIsAStatusAndNotAnError(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.notOpen = map[string]bool{"1": true}

	_, err := rig.walk(t, rig.pinned("1", "g", `gated_tree = g.tree, `))

	require.NoError(t, err)
	report := filterReport(rig.h.commands.ran())
	require.Len(t, report, 1)
	assert.Contains(t, report[0], "report False refused ")
	assert.Contains(t, report[0], "not OPEN")
	assert.Empty(t, rig.merger.landed())
}

func TestMerge_AMemberCallRefusesTheKeywordsThatOnlyAListTakes(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"gate_run":    `gate_run = ["check"], `,
		"integration": `integration = 5, `,
		"hand_off":    `hand_off = True, `,
	}

	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)

			_, err := rig.walk(t, rig.pinned("1", "g", `gated_tree = g.tree, `+extra))

			require.ErrorContains(t, err, "merge with `member` lands one pull request, so it takes no `"+name+"`")
			assert.Empty(t, rig.merger.landed())
		})
	}
}

func TestMerge_AListCallRefusesTheKeywordsThatOnlyAMemberTakes(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"gate", "gated_tree", "expect_tree"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)

			_, err := rig.walk(t, rig.call("1", name+` = "x", `))

			require.ErrorContains(t, err, "merge with `prs` runs its own gate, so it takes no `"+name+"`")
			assert.Empty(t, rig.merger.landed())
			assert.Zero(t, rig.gate.submitted(), "the refusal comes before any gate")
		})
	}
}

func TestMerge_ACallNeedsPrsOrMemberAndNotBoth(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	member := `struct(item = "1", head = "` + rig.heads["1"] + `", closes = [])`

	_, err := rig.walk(t, `merge(base = "main", repo = "o/r", name = "m")`)
	require.ErrorContains(t, err, "merge needs `prs` or `member`")

	_, err = newBatchRig(t).walk(t, `merge(prs = [`+member+`], member = `+member+`, base = "main", repo = "o/r", name = "m")`)
	require.ErrorContains(t, err, "merge takes `prs` or `member`, not both")
}

func TestMerge_AGateWithNoGatedTreeOrAGatedTreeWithNoGateIsRefused(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, rig.pinned("1", "g", ""))
	require.ErrorContains(t, err, "merge with `gate` needs `gated_tree`")

	_, err = newBatchRig(t).walk(t, `out = merge(member = struct(item = "1", head = "x", closes = []), base = "main", repo = "o/r", gated_tree = "t", name = "m")`)
	require.ErrorContains(t, err, "merge with `gated_tree` needs `gate`")
}

func TestMerge_AMemberThatIsNotAWellFormedStructIsRefusedWithItsName(t *testing.T) {
	t.Parallel()

	_, err := newBatchRig(t).walk(t, `merge(member = "1", base = "main", repo = "o/r", name = "m")`)

	require.ErrorContains(t, err, "member: the member has the type string, not struct")
}

func TestMerge_AMemberCallOnABuildWithNoBatchHostFailsWithAFix(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	deps := rig.deps()
	deps.Batch = nil

	engine, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(`merge(member = struct(item = "1", head = "x", closes = []), base = "main", repo = "o/r", name = "m")`)), modeOff())

	require.ErrorContains(t, err, "cannot run merge")
}

func TestMerge_AMergeThatStopsOnTheTreeProofEndsTheCallWithTheLines(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.err = errFake

	_, err := rig.walk(t, rig.pinned("1", "g", `gated_tree = g.tree, `))

	require.ErrorContains(t, err, `merge "m": merge 1: `)
	require.ErrorIs(t, err, errFake)
}

func TestHandOff_TheBuiltinMarksReadyAndWatchesLikeMergeWithHandOff(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.view("1", "OPEN", false), rig.view("1", "MERGED", false))

	body := strings.Replace(strings.Replace(rig.body("1", ""), "out = merge(", "out = hand_off(", 1), "hand_off = True, ", "", 1)
	_, err := rig.walk(t, body)

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "the draft is marked ready once and nothing is drafted again")
	assert.Equal(t, 4, rig.host.pr.reads["1"], "one read to mark it ready, then one read for each round of the watch")
	assert.Len(t, notifyRequests(rig.h), 1, "the owner is told once for the whole wait")
	assert.Equal(t, []string{"report 1   0"}, filterReport(rig.h.commands.ran()))
	assert.Empty(t, rig.merger.landed(), "a hand-off lands nothing")
	assert.Empty(t, rig.gate.ran(), "a hand-off runs no gate")
}

func TestHandOff_ACallThatTheBuiltinCannotReadFailsBeforeAnyStep(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ body, want string }{
		"base that is not text": {body: `hand_off(prs = [struct(item = "1", head = "h", closes = [])], base = len("x"), repo = "o/r", name = "h")`, want: "base"},
		"no name":               {body: `hand_off(prs = [struct(item = "1", head = "h", closes = [])], base = "main", repo = "o/r", name = "")`, want: "needs a `name` that is not empty"},
		"empty prs":             {body: `hand_off(prs = [] + [], base = "main", repo = "o/r", name = "h")`, want: "`prs` is empty"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newHandRig(t)
			_, err := rig.walk(t, c.body)

			require.ErrorContains(t, err, c.want)
			assert.Empty(t, rig.host.pr.reads, "a refused call reads nothing")
		})
	}
}

func TestMerge_AFailedReadOfTheTipAfterTheMergeEndsTheCallWithTheNameOfTheCall(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.tipErrs = []error{nil, errFake, errFake, errFake, errFake}

	_, err := rig.walk(t, rig.pinned("1", "g", `gated_tree = g.tree, `))

	require.ErrorIs(t, err, errFake)
	assert.ErrorContains(t, err, `merge "m"`)
}

func TestKeepGate_ARecordThatIsNotAGateRecordIsAnErrorThatNamesTheStep(t *testing.T) {
	t.Parallel()

	s := &script{}

	require.ErrorContains(t, s.keepGate(map[string]any{stepField: "g#1", "passed": "yes"}), "the record of gate step g#1")
	require.ErrorContains(t, s.keepGate(map[string]any{stepField: "g#2", "head": make(chan int)}), "the record of gate step g#2")
	assert.Empty(t, s.gates, "a record that did not decode is not kept")
	require.NoError(t, s.keepGate(map[string]any{"passed": true}), "an output with no step names no gate")
	assert.Empty(t, s.gates)
}

func TestHandOff_TheBuiltinTakesNoKeywordOfALanding(t *testing.T) {
	t.Parallel()

	for _, keyword := range []string{`hold = "hold"`, `own = {"logins": ["me"]}`, `forbid_text = ["x"]`, `gate_run = ["check"]`, `integration = 5`, `hand_off = True`, `member = 1`} {
		name, _, _ := strings.Cut(keyword, " ")

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newHandRig(t)
			rig.script("1", rig.view("1", "OPEN", true))

			body := strings.Replace(strings.Replace(rig.body("1", keyword+", "), "out = merge(", "out = hand_off(", 1), "hand_off = True, ", "", 1)
			_, err := rig.walk(t, body)

			require.ErrorContains(t, err, "hand_off takes no argument "+name+". Use one of: prs, base, repo, name.")
			assert.Zero(t, rig.host.pr.reads["1"], "a refused call reads nothing")
		})
	}
}
