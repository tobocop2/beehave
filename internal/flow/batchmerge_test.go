package flow

import (
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/batch"
	"github.com/tobocop2/obee/internal/merge"
)

// batchRig is an engine over a real repository with three members: item 2 holds bad.txt, which the gate rejects.
type batchRig struct {
	h      *harness
	repo   repoGit
	heads  map[string]string
	host   *repoHost
	gate   *treeGate
	merger *landMerger
}

func newBatchRig(t *testing.T) *batchRig {
	t.Helper()

	repo, heads := newBatchRepo(t)
	rig := &batchRig{h: newHarness(t), repo: repo, heads: heads}
	rig.host = &repoHost{repo: repo}
	rig.gate = &treeGate{repo: repo}
	rig.merger = &landMerger{repo: repo, script: map[string][]merge.Result{}}

	return rig
}

func (r *batchRig) deps() Deps {
	deps := r.h.deps()
	deps.Batch, deps.Gate, deps.Merge = r.host, r.gate, r.merger

	return deps
}

func (r *batchRig) engine(t *testing.T) *Engine {
	t.Helper()

	engine, err := NewEngine(t.Context(), r.h.opts, r.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

// call writes a batch_merge call over the listed items. extra holds more arguments, each followed by a comma.
func (r *batchRig) call(items, extra string) string {
	var prs []string

	for _, item := range strings.Fields(items) {
		prs = append(prs, `struct(item = "`+item+`", head = "`+r.heads[item]+`")`)
	}

	return `out = batch_merge(prs = [` + strings.Join(prs, ", ") + `], base = "main", repo = "o/r", ` + extra + `name = "b")
command(name = "report", run = ["report", ",".join(out.merged), ",".join([r.item + ":" + r.reason for r in out.rejected]), ",".join([d.item + ":" + d.reason for d in out.deferred])])
`
}

func (r *batchRig) walk(t *testing.T, body string) (ItemResult, error) {
	t.Helper()

	return r.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(body)))
}

func (r *batchRig) mainFiles(t *testing.T) string {
	t.Helper()

	return r.repo.must(t, "ls-tree", "--name-only", "main")
}

func TestBatchMerge_ThreeMembersOneRedTwoLandOnAGreenGate(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, rig.call("1 2 3", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"report 1,3 2:red on top of the merged members "}, rig.h.commands.ran())
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
	assert.Equal(t, "README\none.txt\nthree.txt", rig.mainFiles(t), "main holds the two green members and not bad.txt")

	for _, given := range rig.merger.given {
		assert.NotEmpty(t, given.GatedTree, "each merge compares the base tree with the gated tree")
		require.NotNil(t, given.Intent, "each merge carries the intent that was written before it")
		assert.Equal(t, "OPEN", given.Intent.State)
	}

	assert.Equal(t, []string{"1", "3"}, rig.host.intents)
}

func TestBatchMerge_TheProjectGateReportsTheTreeOfTheUnionItRanOn(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	require.Len(t, rig.gate.ran(), 1, "two green members take one gate")
	assert.Equal(t, rig.repo.must(t, "rev-parse", "main^{tree}"), rig.merger.given[len(rig.merger.given)-1].GatedTree)
}

func TestBatchMerge_AGateWithAnUnknownStepLandsNoMemberEvenWhenItReportsPassed(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.gate.unknownStep = true

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	assert.Empty(t, rig.merger.landed(), "an unknown step is not a pass, so no member lands")
	assert.Equal(t, "README", rig.mainFiles(t))
}

func TestBatchMerge_AGateWithNoStepLandsNoMemberEvenWhenItReportsPassed(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.gate.noSteps = true

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	assert.Empty(t, rig.merger.landed(), "a gate with no step has no result, so no member lands")
	assert.Equal(t, "README", rig.mainFiles(t))
}

func TestBatchMerge_GateRunReplacesTheProjectGateWithAProgram(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, rig.call("1 2 3", `gate_run = ["check-all"], `))

	require.NoError(t, err)
	assert.Empty(t, rig.gate.ran(), "the project gate does not run")
	assert.Equal(t, []string{"check-all", "report 1,2,3  "}, rig.h.commands.ran())
	assert.Equal(t, []string{"1", "2", "3"}, rig.merger.landed())
}

func TestBatchMerge_AMergeableStateThatStaysUnknownDefersTheMemberAfterThreeReads(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	unknown := merge.Result{Stop: merge.Refused, Reason: merge.Unsettled, Lines: []string{"REFUSE: mergeable is UNKNOWN"}}
	rig.merger.script["1"] = []merge.Result{unknown, unknown, unknown}
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.call("1 3", ""))))

	require.NoError(t, err)
	assert.Contains(t, rig.h.commands.ran()[0], "1:mergeable is UNKNOWN after 3 reads of 1")
	assert.Equal(t, []string{"1", "1", "1"}, rig.merger.landed())
}

func TestBatchMerge_AGateRunThatIsNotAListIsRefused(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, rig.call("1", `gate_run = "x" + "", `))

	require.ErrorContains(t, err, "gate_run must be a list of strings")
	assert.Empty(t, rig.merger.asked)
}

func TestBatchMerge_AMemberThatTheMergeRefusesIsRejectedWithItsLastLine(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.script["1"] = []merge.Result{{Stop: merge.OwnWork, Lines: []string{"first", "OWN WORK: leave it to the owner"}}}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	assert.Contains(t, rig.h.commands.ran()[0], "1:OWN WORK: leave it to the owner")
}

func TestBatchMerge_ATreeProofStopEndsTheRunWithTheLines(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Reason: merge.TreeProof, Lines: []string{"STOP: main tip holds another tree"}}}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.ErrorIs(t, err, errMergeRefused)
	require.ErrorContains(t, err, "STOP: main tip holds another tree")
	assert.Empty(t, rig.h.commands.ran(), "the report after the call never runs")
}

func TestBatchMerge_ARefusalThatOnlyLooksLikeAStopLineIsARejectionNotAStop(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Lines: []string{"STOP: said by a plain refusal", "REFUSE: mergeable is UNKNOWN"}}}

	_, err := rig.walk(t, rig.call("1", ""))

	require.NoError(t, err, "only the typed reason decides; the text of a line does not")
	assert.Contains(t, rig.h.commands.ran()[0], "1:REFUSE: mergeable is UNKNOWN")
}

func TestBatchMerge_AMergeStepThatFailsCallsTheForgeMergeOnce(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.err = errFake

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.ErrorContains(t, err, errFake.Error())
	rig.merger.mu.Lock()
	defer rig.merger.mu.Unlock()
	assert.Equal(t, []string{"1"}, rig.merger.asked, "a merge writes to the forge, so its step is never retried")
}

func TestBatchMerge_AFailureOfAHostCallEndsTheRunWithThatError(t *testing.T) {
	t.Parallel()

	for name, set := range map[string]func(*batchRig){
		"git":      func(r *batchRig) { r.host.gitErr = errFake },
		"checkout": func(r *batchRig) { r.host.checkErr = errFake },
		"intent":   func(r *batchRig) { r.host.intentErr = errFake },
		"merger":   func(r *batchRig) { r.merger.err = errFake },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)
			set(rig)

			_, err := rig.walk(t, rig.call("1 3", ""))

			require.ErrorContains(t, err, errFake.Error())
		})
	}
}

func TestBatchMerge_ACallThatTheBuildOrTheArgumentsCannotServeFailsWithAFix(t *testing.T) {
	t.Parallel()

	bad := func(r *batchRig, prs string) string {
		return `batch_merge(prs = ` + prs + `, base = "main", repo = "o/r", name = "b")`
	}
	member := func(r *batchRig, extra string) string {
		return `struct(item = "1", head = "` + r.heads["1"] + `"` + extra + `)`
	}

	cases := map[string]struct {
		body  func(r *batchRig) string
		build func(r *batchRig, d *Deps)
		want  string
	}{
		"no name": {body: func(r *batchRig) string {
			return `batch_merge(prs = [` + member(r, "") + `], base = "main", repo = "o/r", name = "")`
		}, want: "needs a `name` that is not empty"},
		"repeated name": {body: func(r *batchRig) string {
			return r.call("1", "") + "\n" + r.call("3", "")
		}, want: `name "b" is used twice`},
		"empty prs":      {body: func(r *batchRig) string { return bad(r, "[]") }, want: "`prs` is empty"},
		"prs not a list": {body: func(r *batchRig) string { return bad(r, `"x" + ""`) }, want: "`prs` must be a list"},
		"member not a struct": {body: func(r *batchRig) string {
			return bad(r, `["x"]`)
		}, want: "`prs` holds a string"},
		"member without head": {body: func(r *batchRig) string {
			return bad(r, `[struct(item = "1")]`)
		}, want: "has no `head`"},
		"member with empty item": {body: func(r *batchRig) string {
			return bad(r, `[struct(item = "", head = "h")]`)
		}, want: "`item` of a member of `prs` must be text that is not empty"},
		"empty base": {body: func(r *batchRig) string {
			return `batch_merge(prs = [` + member(r, "") + `], base = "", repo = "o/r", name = "b")`
		}, want: "base must not be empty"},
		"empty repo": {body: func(r *batchRig) string {
			return `batch_merge(prs = [` + member(r, "") + `], base = "main", repo = "", name = "b")`
		}, want: "repo must not be empty"},
		"gate_run not a list": {body: func(r *batchRig) string {
			return r.call("1", `gate_run = 3, `)
		}, want: "gate_run"},
		"no batch host": {body: func(r *batchRig) string { return r.call("1", "") }, build: func(_ *batchRig, d *Deps) { d.Batch = nil }, want: "cannot run batch_merge"},
		"no merger":     {body: func(r *batchRig) string { return r.call("1", "") }, build: func(_ *batchRig, d *Deps) { d.Merge = nil }, want: errNoMerger.Error()},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)
			deps := rig.deps()

			if c.build != nil {
				c.build(rig, &deps)
			}

			engine, err := NewEngine(t.Context(), rig.h.opts, deps)
			require.NoError(t, err)
			require.NoError(t, engine.Launch())
			t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

			_, err = engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(c.body(rig))))

			require.ErrorContains(t, err, c.want)
		})
	}
}

func TestBatchMerge_ACallWithNoArgumentsIsRefusedWithTheMissingName(t *testing.T) {
	t.Parallel()

	_, err := newBatchRig(t).walk(t, `batch_merge(name = "b")`)

	require.ErrorContains(t, err, "prs")
}

func TestBatchMerge_AFailedTipReadInsideTheIntentStepEndsTheRun(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.tipErrs = []error{nil, nil, errFake, errFake, errFake}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, rig.merger.landed())
}

func TestBatchMerge_AFailedReadInsideTheIntentStepIsRetriedLikeTheOtherReads(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.tipErrs = []error{nil, nil, errFake}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
}

func TestBatchMerge_ACanceledRunStopsTheWaitBetweenReadsOfAnUnknownMergeableState(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.sleep = time.Hour
	unknown := merge.Result{Stop: merge.Refused, Reason: merge.Unsettled, Lines: []string{"REFUSE: mergeable is UNKNOWN"}}
	rig.merger.script["1"] = []merge.Result{unknown, unknown, unknown}
	engine := rig.engine(t)
	done := runInBackground2(engine, writeFlow(t, bodyFlow(rig.call("1 3", ""))))

	waitForTheSleep(t, engine)
	require.NoError(t, rig.h.client(t, testVersion).Cancel(testItem))
	rig.h.tick(t)

	outcome := <-done

	require.Error(t, outcome.err)
	assert.Len(t, rig.merger.landed(), 1, "the run ends in the wait and reads no more")
}

func TestBatchMerge_TheItemGetsItsWorktreeBeforeTheFirstGitCall(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.trees.late = &lateFolder{dir: t.TempDir()}
	rig.host.trees = rig.h.trees

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	assert.NotEmpty(t, rig.host.folderAtGit, "Ensure made the worktree before the host used it")
}

func TestBatchMerge_AnItemWithoutAFolderFailsBeforeAnyGitCallWithAFix(t *testing.T) {
	t.Parallel()

	empty := newBatchRig(t)
	empty.h.trees.dir = ""
	broken := newBatchRig(t)
	broken.h.trees.err = errFake

	_, err := empty.walk(t, empty.call("1", ""))
	require.ErrorContains(t, err, "item "+testItem+" of project "+testProject+" has no folder for the union")

	_, err = broken.walk(t, broken.call("1", ""))
	require.ErrorIs(t, err, errFake)
	assert.Empty(t, empty.merger.landed())
	assert.Empty(t, broken.merger.landed())
}

func TestBatchMerge_AMemberWhosePullRequestIsNotOpenIsRejectedAndTheRestLand(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.notOpen = map[string]bool{"1": true}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err, "a member that was closed by hand is a member problem, and not a failure of the run")
	assert.Contains(t, rig.h.commands.ran()[0], "1:the pull request of item 1 is MERGED, not OPEN")
	assert.Equal(t, []string{"3"}, rig.merger.landed())
}

func TestBatchMerge_ATransientRefusalDefersTheMemberAndTheMergerRunsOnce(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	failed := merge.Result{Stop: merge.Refused, Reason: merge.Transient, Lines: []string{"first", "REFUSE: MergePR: down"}}
	rig.merger.script["1"] = []merge.Result{failed, failed, failed}
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.call("1 3", ""))))

	require.NoError(t, err)
	assert.Contains(t, rig.h.commands.ran()[0], "1:REFUSE: MergePR: down")
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the merge step of a member never runs twice in one attempt, and the later member waits")

	for _, name := range stepNames(t, engine) {
		assert.NotContains(t, name, readSeparator, "no later read was made")
	}
}

func TestBatchMerge_EachMemberRecordsItsIntentStepBeforeItsMergeStep(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.call("1 3", ""))))

	require.NoError(t, err)

	var own []string

	for _, name := range stepNamesOf(t, engine, testRun) {
		if strings.HasPrefix(name, "batch:b.intent#") || name == exclusiveName {
			own = append(own, name)
		}
	}

	assert.Equal(t, []string{exclusiveName, "batch:b.intent#1", exclusiveName, "batch:b.intent#3", exclusiveName}, own, "the gate, then the intent and the merge of each member")
}

func TestBatchMerge_ACancelWhileTheMergeStepRunsThenAResumeMergesTheMemberOnce(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.onceOnly, rig.merger.cancelOn = true, "1"
	rig.merger.cancel = func() {
		if err := rig.h.client(t, testVersion).Cancel(testItem); err != nil {
			t.Error(err)
		}

		rig.h.tick(t)
	}
	file := writeFlow(t, bodyFlow(rig.call("1 3", "")))
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)

	require.Error(t, err)
	require.NoError(t, rig.h.client(t, testVersion).Resume(testItem))

	_, err = engine.RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "1", "3"}, rig.merger.landed(), "the cut-off merge step ran again")
	assert.Equal(t, []string{"1", "3"}, rig.host.intents, "the intent step is kept, so the merge step has the intent of the first run")
	assert.Equal(t, 1, strings.Count(rig.repo.must(t, "log", "--format=%s", "main"), "land 1"), "the member landed once")
	assert.Len(t, rig.gate.ran(), 1, "the union gate ran once")
}

func TestBatchMerge_TheStepNameOfALaterReadNeverEqualsTheNameOfAnotherItem(t *testing.T) {
	t.Parallel()

	b := &batchBase{call: batchCall{name: "n"}}

	assert.NotEqual(t, b.stepName("merge", "bd-a3f8", 2), b.stepName("merge", "bd-a3f8.2", 1))
	assert.Equal(t, "batch:n.merge#bd-a3f8", b.stepName("merge", "bd-a3f8", 1))
}

func TestBatchMerge_AGateStepThatFailsIsRunOnce(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.checkErr = errFake

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.ErrorIs(t, err, errFake)
	assert.Equal(t, 1, rig.host.checkoutCalls(), "the gate step checks the union out, and a failure is recorded without a second run")
}

func TestBatchMerge_AGateOrAMergeWithAContextThatIsNotARunIsRefused(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	s := &script{e: rig.engine(t)}

	_, err := batchGate{s: s}.Run(t.Context(), batch.GateInput{})
	require.ErrorIs(t, err, errNotRunContext)

	_, err = (&batchBase{s: s}).mergeReads(t.Context(), batch.Member{}, MergeSettings{})
	require.ErrorIs(t, err, errNotRunContext)
}

func TestBatchMerge_ACallOutsideAWorkflowRunIsRefused(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	engine := rig.engine(t)
	s := &script{e: engine, ctx: engine.ctx}

	require.ErrorIs(t, s.batchScope("b"), errNoScope)
}

func TestBatchMerge_ARecordedGateThatRanOnAnotherTreeStopsTheReplayToo(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.gate.lie = "0000000000000000000000000000000000000000"
	file := writeFlow(t, bodyFlow(rig.call("1 3", "")))
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "the recorded gate ran on tree "+rig.gate.lie)

	rig.gate.mu.Lock()
	rig.gate.lie = ""
	rig.gate.mu.Unlock()

	require.NoError(t, rig.h.client(t, testVersion).Resume(testItem))

	_, err = engine.RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "the recorded gate ran on tree", "the replay reads the recorded gate and does not run it again")
	assert.Len(t, rig.gate.ran(), 1)
}

func TestBatchMerge_ATransientFailureOfTheTipReadIsRetriedInsideTheStep(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.tipErrs = []error{errFake}
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.call("1 3", ""))))

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	for _, step := range steps {
		assert.NoError(t, step.Error, step.StepName, "no step records the transient failure")
	}
}

func TestBatchMerge_AFailureThatStaysIsRecordedAndAResumeRunsTheStepAgain(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.tipErrs = []error{errFake, errFake, errFake}
	file := writeFlow(t, bodyFlow(rig.call("1 3", "")))
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)

	require.ErrorIs(t, err, errFake)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	var recorded error

	for _, step := range steps {
		if step.StepName == "batch:b.tip#1" {
			recorded = step.Error
		}
	}

	require.Error(t, recorded, "DBOS records the error of a step that ended in one, and a replay returns it without running the step")

	reads := rig.host.tipReads()

	require.NoError(t, rig.h.client(t, testVersion).Resume(testItem))

	_, err = engine.RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Greater(t, rig.host.tipReads(), reads, "the resume drops the failed record, so the tip is read again")
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
}
