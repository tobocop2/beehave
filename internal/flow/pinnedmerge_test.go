package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

// member is the struct of the member of a union for the item, at its head.
func (r *batchRig) member(item string) string {
	return `struct(item = "` + item + `", head = "` + r.heads[item] + `", closes = [])`
}

// reasonOf is a command that reports the reason of the result out of a merge call.
const reasonOf = `command(name = "report", run = ["report", out.reason])
`

func TestMerge_AMemberWithAPassedGateOfThisRunMergesAndGetsItsStatuses(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1")

	_, err := rig.walk(t, rig.union("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Equal(t, []string{"beehave/gate success " + rig.heads["1"]}, rig.merger.statusesGiven())
	require.Len(t, rig.merger.given, 1)
	assert.Equal(t, rig.repo.must(t, "rev-parse", "HEAD^{tree}"), rig.merger.given[0].GatedTree, "the merge compares the base tree with the tree of the gate")
	assert.Equal(t, []string{"report 1    False"}, filterReport(rig.h.commands.ran()))
}

func TestMerge_AnApprovedMemberAlsoGetsTheReviewStatus(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1")

	body := `g = gate(name = "g")
out = merge(members = [struct(item = "1", head = "` + rig.heads["1"] + `", approved = True, closes = [7])], base = "main", repo = "o/r", gate = g, name = "m")
`

	_, err := rig.walk(t, body)

	require.NoError(t, err)
	assert.Equal(t, []string{"beehave/review success " + rig.heads["1"], "beehave/gate success " + rig.heads["1"]}, rig.merger.statusesGiven())
	assert.Equal(t, []int{7}, rig.merger.given[0].Closes)
}

func TestMerge_AStatusIsPostedOnlyForAPassedGateOfThisRun(t *testing.T) {
	t.Parallel()

	madeUp := `struct(step = "gate:g#1", passed = True, head = "h", tree = "t", steps = [{"name": "command", "status": "pass"}])`

	cases := map[string]struct{ gate, after, want string }{
		"a value that no gate call made":         {gate: madeUp, want: "no gate call of this run recorded it"},
		"a gate call that comes after the merge": {gate: madeUp, after: "\ng = gate(name = \"g\")", want: "no gate call of this run recorded it"},
		"a value with no step":                   {gate: `struct(passed = True)`, want: "is not the result of a gate call"},
		"a value that is not a struct":           {gate: `"gate:g#1"`, want: "`gate` must be the result of a gate call, not string"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)

			_, err := rig.walk(t, `out = merge(members = [`+rig.member("1")+`], base = "main", repo = "o/r", gate = `+c.gate+`, name = "m")`+c.after)

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
out = merge(members = [` + rig.member("1") + `], base = "main", repo = "o/r", gate = ` + forged + `, name = "m")
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
	rig.foldOf("1")

	_, err := rig.walk(t, rig.union("1", ""))

	require.ErrorContains(t, err, "did not pass")
	assert.Empty(t, rig.merger.statusesGiven())
	assert.Empty(t, rig.merger.landed())
}

func TestMerge_AGateThatRecordedNoTreeIsNotTrusted(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.gate.noTree = true
	rig.foldOf("1")

	_, err := rig.walk(t, rig.union("1", ""))

	require.ErrorContains(t, err, "recorded no tree, so no union can be compared with it")
	assert.Empty(t, rig.merger.statusesGiven())
	assert.Empty(t, rig.merger.landed())
}

func TestMerge_AGitTooOldForTheFoldEndsTheCallBeforeAnyStatus(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.version = "git version 2.30.0"
	rig.foldOf("1")

	_, err := rig.walk(t, rig.union("1", ""))

	require.ErrorContains(t, err, "git 2.38 or newer is needed for merge")
	assert.Empty(t, rig.merger.statusesGiven())
	assert.Empty(t, rig.merger.landed())
}

func TestMerge_AReplayOfTheRunTrustsTheGateThatTheRecordHolds(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1")
	rig.host.tipErrs = []error{errFake, errFake, errFake}
	file := writeFlow(t, bodyFlow(rig.union("1", "")))
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file, modeOff())

	require.ErrorIs(t, err, errFake, "the tip read of the plan step fails after the gate step was recorded")
	assert.Empty(t, rig.merger.landed())

	_, err = resumeAndJoin(t, engine, rig.h.client(t, testVersion))

	require.NoError(t, err)
	assert.Len(t, rig.gate.ran(), 1, "the replay reads the recorded gate and runs no gate again")
	assert.Equal(t, []string{"beehave/gate success " + rig.heads["1"]}, rig.merger.statusesGiven(), "the gate of the record is trusted")
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestMerge_AGateOfThisRunOnAnotherTreeThanTheFoldPostsNoStatusAndMergesNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.repo.must(t, "switch", "-q", "m3") // the gate runs on the head of item 3, and the call merges item 1

	_, err := rig.walk(t, rig.union("1", ""))

	require.NoError(t, err)
	assert.Empty(t, rig.merger.statusesGiven(), "no status is posted for a gate that ran on another tree")
	assert.Empty(t, rig.merger.given, "the merge call is never made")
	assert.Equal(t, []string{"report     True"}, filterReport(rig.h.commands.ran()))
}

func TestMerge_AMemberThatConflictsWithTheBaseTipMovesTheCallBeforeAnyStatus(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.repo.must(t, "switch", "-q", "main")
	rig.repo.must(t, "switch", "-q", "-c", "clash")
	require.NoError(t, os.WriteFile(filepath.Join(rig.repo.dir, "README"), []byte("clash\n"), 0o600))
	rig.repo.must(t, "commit", "-q", "-am", "clash")

	rig.heads["9"] = rig.repo.must(t, "rev-parse", "HEAD")

	rig.repo.must(t, "switch", "-q", "main")
	require.NoError(t, os.WriteFile(filepath.Join(rig.repo.dir, "README"), []byte("main moved\n"), 0o600))
	rig.repo.must(t, "commit", "-q", "-am", "main moved")

	_, err := rig.walk(t, rig.union("9", "")+reasonOf)

	require.NoError(t, err)
	assert.Empty(t, rig.merger.statusesGiven())
	assert.Empty(t, rig.host.intents, "no pull request is read for a union that does not merge cleanly")
	assert.Contains(t, filterReport(rig.h.commands.ran())[1], "conflicts with the base tip and the members before it")
}

func TestMerge_AGitThatFailsEndsTheCallAndAHeadThatIsNotInTheRepositoryMovesIt(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.gitErr = errFake
	rig.foldOf("1")

	_, err := rig.walk(t, rig.union("1", ""))

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, rig.merger.statusesGiven())

	rig = newBatchRig(t)
	rig.heads["1"] = strings.Repeat("a", 40)

	_, err = rig.walk(t, rig.union("1", "")+reasonOf)

	require.NoError(t, err)
	assert.Empty(t, rig.merger.statusesGiven())
	assert.Empty(t, rig.merger.landed())
	assert.Contains(t, filterReport(rig.h.commands.ran())[1], "is not in the repository; fetch it, then gate the union again")
}

func TestMerge_ACallWithNoGateMergesWithNoStatusAndNoTreeProof(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, `out = merge(members = [`+rig.member("1")+`], base = "main", repo = "o/r", name = "m")
`)

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Empty(t, rig.merger.statusesGiven())
	assert.Empty(t, rig.merger.given[0].GatedTree)
}

func TestMerge_TheResultOfAUnionHoldsTheTipAfterTheCall(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1")

	_, err := rig.walk(t, rig.union("1", "")+`command(name = "tip", run = ["tip", out.tip.commit, out.tip.tree])
`)

	require.NoError(t, err)

	want := "tip " + rig.repo.must(t, "rev-parse", "main") + " " + rig.repo.must(t, "rev-parse", "main^{tree}")

	assert.Contains(t, rig.h.commands.ran(), want, "the tip is read after the merge")
}

func TestMerge_ARefusalOfOneMemberIsAResultAndNotAnError(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.notOpen = map[string]bool{"1": true}
	rig.foldOf("1")

	_, err := rig.walk(t, rig.union("1", ""))

	require.NoError(t, err)
	report := filterReport(rig.h.commands.ran())
	require.Len(t, report, 1)
	assert.Contains(t, report[0], "report  1:")
	assert.Contains(t, report[0], "not OPEN")
	assert.Empty(t, rig.merger.landed())
}

func TestMerge_ACallNeedsMembers(t *testing.T) {
	t.Parallel()

	_, err := newBatchRig(t).walk(t, `merge(base = "main", repo = "o/r", name = "m")`)
	require.ErrorContains(t, err, "merge needs `members`")
}

func TestMerge_AMemberThatIsNotAWellFormedStructIsRefusedWithItsPlace(t *testing.T) {
	t.Parallel()

	_, err := newBatchRig(t).walk(t, `merge(members = ["1"], base = "main", repo = "o/r", name = "m")`)

	require.ErrorContains(t, err, "members[0]")
}

func TestMerge_AUnionCallWithABaseThatTheRunComputesAsEmptyIsRefused(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, `merge(members = [`+rig.member("1")+`], base = "" + "", repo = "o/r", name = "m")`)

	require.ErrorContains(t, err, "base must not be empty")
	assert.Empty(t, rig.merger.landed())
}

func TestMerge_AUnionCallOnABuildWithNoBatchHostFailsWithAFix(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	deps := rig.deps()
	deps.Batch = nil

	engine, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(`merge(members = [struct(item = "1", head = "x", closes = [])], base = "main", repo = "o/r", name = "m")`)), modeOff())

	require.ErrorContains(t, err, "cannot run merge")
}

func TestMerge_AMergeThatStopsOnTheTreeProofEndsTheCallWithTheNameOfTheMember(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.err = errFake
	rig.foldOf("1")

	_, err := rig.walk(t, rig.union("1", ""))

	require.ErrorContains(t, err, `merge "u": merge 1: `)
	require.ErrorIs(t, err, errFake)
}

func TestHandOff_ACallThatTheBuiltinCannotReadFailsBeforeAnyStep(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ body, want string }{
		"base that is not text": {body: `hand_off(prs = [struct(item = "1", head = "h", closes = [])], base = len("x"), repo = "o/r", name = "h")`, want: "base"},
		"no name":               {body: `hand_off(prs = [struct(item = "1", head = "h", closes = [])], base = "main", repo = "o/r", name = "")`, want: "needs a `name` that is not empty"},
		"empty prs":             {body: `hand_off(prs = [] + [], base = "main", repo = "o/r", name = "h")`, want: "`prs` is empty"},
		"empty base":            {body: `hand_off(prs = [struct(item = "1", head = "h", closes = [])], base = "" + "", repo = "o/r", name = "h")`, want: "base must not be empty"},
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
	rig.foldOf("1")
	rig.host.tipErrs = []error{nil, nil, errFake, errFake, errFake, errFake}

	_, err := rig.walk(t, rig.union("1", ""))

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, `merge "u"`)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the read that fails is the one after the merge")
}

func TestKeepGate_ARecordThatDoesNotDecodeOrNamesNoStepIsNotKeptSoAMergeFindsNoGate(t *testing.T) {
	t.Parallel()

	s := &script{}

	s.keepGate(map[string]any{stepField: "g#1", "passed": "yes"})
	s.keepGate(map[string]any{stepField: "g#2", "head": make(chan int)})
	s.keepGate(map[string]any{"passed": true})
	assert.Empty(t, s.gates, "nothing of the three is kept")

	s.keepGate(map[string]any{stepField: "g#3", "passed": true, "tree": "t"})
	assert.Equal(t, gateRecord{Passed: true, Tree: "t"}, s.gates["g#3"])
}

func TestMerge_ACallThatTheCheckDoesNotSeeIsRefusedByTheRunWithTheSameForm(t *testing.T) {
	t.Parallel()

	member := `struct(item = "1", head = "h", closes = [])`
	cases := map[string]struct{ args, want string }{
		"prs with members": {args: `"base": "main", "prs": [], "members": [` + member + `]`, want: workflow.MergeBatchRefusal("prs")},
		"gate_run":         {args: `"base": "main", "gate_run": [], "members": [` + member + `]`, want: workflow.MergeBatchRefusal("gate_run")},
		"empty base":       {args: `"members": [` + member + `], "base": "" + ""`, want: "base must not be empty"},
		"no members":       {args: `"base": "main"`, want: "merge needs `members`"},
		"unknown keyword":  {args: `"base": "main", "wrong": 1, "members": [` + member + `]`, want: `merge: unexpected keyword argument "wrong"`},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)
			_, err := rig.walk(t, `merge(**dict({"repo": "o/r", "name": "m", `+c.args+`}))`)

			require.ErrorContains(t, err, c.want)
			assert.Empty(t, rig.merger.landed())
		})
	}
}

func TestHandOff_TheBuiltinTakesNoKeywordOfALanding(t *testing.T) {
	t.Parallel()

	for _, keyword := range []string{`hold = "hold"`, `own = {"logins": ["me"]}`, `forbid_text = ["x"]`, `gate_run = ["check"]`, `integration = 5`, `hand_off = True`, `members = 1`} {
		name, _, _ := strings.Cut(keyword, " ")

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newHandRig(t)
			rig.script("1", rig.view("1", "OPEN", true))

			_, err := rig.walk(t, rig.body("1", keyword+", "))

			require.ErrorContains(t, err, "hand_off takes no argument "+name+". Use one of: prs, base, repo, name.")
			assert.Zero(t, rig.host.pr.reads["1"], "a refused call reads nothing")
		})
	}
}
