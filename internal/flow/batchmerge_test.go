package flow

import (
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/workflow"
)

// batchRig is an engine over a real repository with three members: item 2 holds bad.txt, which the gate rejects.
type batchRig struct {
	h      *harness
	repo   repoGit
	heads  map[string]string
	host   *repoHost
	gate   *treeGate
	merger *landMerger
	t      *testing.T
}

func newBatchRig(t *testing.T) *batchRig {
	t.Helper()

	repo, heads := newBatchRepo(t)
	rig := &batchRig{h: newHarness(t), repo: repo, heads: heads, t: t}
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

// call checks out the union of the listed items, then writes a gate call and one merge call of their members with that gate. extra holds more arguments,
// each followed by a comma. The report command shows the merged, the rejected and the deferred members.
func (r *batchRig) call(items, extra string) string {
	r.foldOf(strings.Fields(items)...)

	return r.membersCall(r.members(items), extra) + `command(name = "report", run = ["report", ",".join(out.merged), ",".join([r.item + ":" + r.reason for r in out.rejected]), ",".join([d.item + ":" + d.reason for d in out.deferred])])
`
}

// members writes the member structs of the listed items with no closed issue.
func (r *batchRig) members(items string) string {
	var members []string

	for _, item := range strings.Fields(items) {
		members = append(members, `struct(item = "`+item+`", head = "`+r.heads[item]+`", closes = [])`)
	}

	return "[" + strings.Join(members, ", ") + "]"
}

// membersCall writes a gate call and one merge call of the given members with that gate.
func (r *batchRig) membersCall(members, extra string) string {
	return `g = gate(name = "g")
out = merge(members = ` + members + `, base = "main", repo = "o/r", gate = g, ` + extra + `name = "b")
`
}

func (r *batchRig) walk(t *testing.T, body string) (ItemResult, error) {
	t.Helper()

	return r.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(body)), modeOff())
}

func (r *batchRig) mainFiles(t *testing.T) string {
	t.Helper()

	return r.repo.must(t, "ls-tree", "--name-only", "main")
}

// reportLine is the one report line of the run.
func (r *batchRig) reportLine(t *testing.T) string {
	t.Helper()

	lines := filterReport(r.h.commands.ran())
	require.Len(t, lines, 1, "one report: %v", r.h.commands.ran())

	return lines[0]
}

func TestMerge_TheClosesOfAMemberReachTheMergeOfThatMemberOnly(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1", "3")
	members := `[struct(item = "1", head = "` + rig.heads["1"] + `", closes = [12, 7]), struct(item = "3", head = "` + rig.heads["3"] + `", closes = [])]`

	_, err := rig.walk(t, rig.membersCall(members, ""))

	require.NoError(t, err)
	require.Len(t, rig.merger.given, 2)
	assert.Equal(t, []int{12, 7}, rig.merger.given[0].Closes)
	assert.Empty(t, rig.merger.given[1].Closes)
}

func TestMerge_AMergeableStateThatStaysUnknownDefersTheMemberAfterThreeReads(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	unknown := merge.Result{Stop: merge.Refused, Reason: merge.Unsettled, Lines: []string{"REFUSE: mergeable is UNKNOWN"}}
	rig.merger.script["1"] = []merge.Result{unknown, unknown, unknown}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	assert.Contains(t, rig.reportLine(t), "1:mergeable is UNKNOWN after 3 reads of 1")
	assert.Equal(t, []string{"1", "1", "1"}, rig.merger.landed())
}

func TestMerge_ARefusedMemberIsRejectedWithEveryLineOfTheRefusal(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Lines: []string{"OK: x", "REFUSE: a", "  b"}, Refusal: []string{"REFUSE: a", "  b"}}}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	assert.Contains(t, rig.reportLine(t), "1:REFUSE: a\n  b")
	assert.NotContains(t, rig.reportLine(t), "OK: x", "a line of a guard that passed is not the cause")
}

func TestMerge_EachMemberMergesWithItsOwnApproval(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1", "3")
	members := `[struct(item = "1", head = "` + rig.heads["1"] + `", approved = True, closes = []), struct(item = "3", head = "` + rig.heads["3"] + `", closes = [])]`

	_, err := rig.walk(t, rig.membersCall(members, ""))

	require.NoError(t, err)
	require.Equal(t, []string{"1", "3"}, rig.merger.landed())

	approved := map[string]bool{}
	for i, item := range rig.merger.landed() {
		approved[item] = rig.merger.given[i].Approved
	}

	assert.Equal(t, map[string]bool{"1": true, "3": false}, approved, "the approval of member 1 reaches only the merge of member 1")
}

func TestMerge_AnOwnMemberWithNoApprovalIsRefusedAsOwnWork(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	world := &shipWorld{pull: shipPull(t, rig.heads["1"], map[string]any{"author": map[string]any{"login": shipOwner}})}
	merger := &fakeMerge{over: mergerOver(world)}

	deps := rig.deps()
	deps.Merge = merger

	engine, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	own := `own = {"logins": ["` + shipOwner + `"], "emails": ["` + shipMail + `"]}, `
	_, err = engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.call("1", own))), modeOff())

	require.NoError(t, err)
	assert.Contains(t, rig.reportLine(t), "1:", "member 1 is rejected")
	assert.Contains(t, rig.reportLine(t), "The owner merges it in the forge")
	assert.Equal(t, 1, merger.askedCount(), "the merge guard saw the member once")
	assert.Empty(t, world.writes, "a refused member has had no write on the forge")
	assert.Equal(t, "README", rig.mainFiles(t), "nothing landed")
}

func TestMerge_ATreeProofStopEndsTheRunWithTheLines(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Reason: merge.TreeProof, Lines: []string{"STOP: main tip holds another tree"}}}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.ErrorIs(t, err, errMergeRefused)
	require.ErrorContains(t, err, "STOP: main tip holds another tree")
	assert.Empty(t, filterReport(rig.h.commands.ran()), "the report after the call never runs")
}

func TestMerge_ARefusalThatOnlyLooksLikeAStopLineIsARejectionNotAStop(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Lines: []string{"STOP: said by a plain refusal", "REFUSE: mergeable is UNKNOWN"}, Refusal: []string{"REFUSE: mergeable is UNKNOWN"}}}

	_, err := rig.walk(t, rig.call("1", ""))

	require.NoError(t, err, "only the typed reason decides; the text of a line does not")
	assert.Contains(t, rig.reportLine(t), "1:REFUSE: mergeable is UNKNOWN")
}

func TestMerge_AMergeStepThatFailsCallsTheForgeMergeOnce(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.err = errFake

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.ErrorContains(t, err, errFake.Error())
	rig.merger.mu.Lock()
	defer rig.merger.mu.Unlock()
	assert.Equal(t, []string{"1"}, rig.merger.asked, "a merge writes to the forge, so its step is never retried")
}

func TestMerge_AFailureOfAHostCallEndsTheRunWithThatError(t *testing.T) {
	t.Parallel()

	for name, set := range map[string]func(*batchRig){
		"git":    func(r *batchRig) { r.host.gitErr = errFake },
		"intent": func(r *batchRig) { r.host.intentErr = errFake },
		"merger": func(r *batchRig) { r.merger.err = errFake },
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

func TestMerge_ACallThatTheBuildOrTheArgumentsCannotServeFailsWithAFix(t *testing.T) {
	t.Parallel()

	bad := func(r *batchRig, members string) string {
		return r.membersCall(members, "")
	}
	member := func(r *batchRig, extra string) string {
		return `[struct(item = "1", head = "` + r.heads["1"] + `", closes = []` + extra + `)]`
	}

	cases := map[string]struct {
		body  func(r *batchRig) string
		build func(r *batchRig, d *Deps)
		want  string
	}{
		"no name": {body: func(r *batchRig) string {
			return `merge(members = ` + member(r, "") + `, base = "main", repo = "o/r", name = "")`
		}, want: "needs a `name` that is not empty"},
		"repeated name": {body: func(r *batchRig) string {
			return r.call("1", "") + `merge(members = ` + member(r, "") + `, base = "main", repo = "o/r", name = "b")`
		}, want: `name "b" is used twice`},
		"members not a list": {body: func(r *batchRig) string { return bad(r, `"x" + ""`) }, want: "`members` must be a list"},
		"member not a struct": {body: func(r *batchRig) string {
			return bad(r, `["x"]`)
		}, want: "the member has the type string, not struct"},
		"member without head": {body: func(r *batchRig) string {
			return bad(r, `[struct(item = "1")]`)
		}, want: "has no `head`"},
		"member with approved that is not a bool": {body: func(r *batchRig) string {
			return bad(r, member(r, `, approved = "yes"`))
		}, want: "`approved` of a member of `prs` must be a bool, not string"},
		"computed empty base": {body: func(r *batchRig) string {
			return `merge(members = ` + member(r, "") + `, base = "" + "", repo = "o/r", name = "b")`
		}, want: "base must not be empty"},
		"no batch host": {body: func(r *batchRig) string { return r.call("1", "") }, build: func(_ *batchRig, d *Deps) { d.Batch = nil }, want: "cannot run merge"},
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

			_, err = engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(c.body(rig))), modeOff())

			require.ErrorContains(t, err, c.want)
		})
	}
}

func TestMerge_ACallWithNoArgumentsIsRefusedWithTheMissingName(t *testing.T) {
	t.Parallel()

	_, err := newBatchRig(t).walk(t, `merge(name = "b")`)

	require.ErrorContains(t, err, "merge needs the `base` keyword")
	require.ErrorContains(t, err, "merge needs the `repo` keyword")
}

func TestMerge_ACallWithAKeywordOfTheBatchIsRefusedAndNamesBatchMerge(t *testing.T) {
	t.Parallel()

	for _, keyword := range workflow.BatchKeywords() {
		t.Run(keyword, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)

			_, err := rig.walk(t, `merge(members = `+rig.members("1")+`, base = "main", repo = "o/r", name = "b", `+keyword+` = 1 + 0)`)

			require.ErrorContains(t, err, workflow.MergeBatchRefusal(keyword))
			assert.Empty(t, rig.merger.landed())
		})
	}
}

func TestMerge_AFailedTipReadInsideTheIntentStepEndsTheRun(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.tipErrs = []error{nil, errFake, errFake, errFake}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, rig.merger.landed())
}

func TestMerge_AFailedReadInsideTheIntentStepIsRetriedLikeTheOtherReads(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.tipErrs = []error{nil, errFake}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
}

func TestMerge_ACanceledRunStopsTheWaitBetweenReadsOfAnUnknownMergeableState(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.sleep = time.Hour
	unknown := merge.Result{Stop: merge.Refused, Reason: merge.Unsettled, Lines: []string{"REFUSE: mergeable is UNKNOWN"}}
	rig.merger.script["1"] = []merge.Result{unknown, unknown, unknown}
	engine := rig.engine(t)
	done := runInBackground2(engine, writeFlow(t, bodyFlow(rig.call("1 3", ""))))

	waitOn(t, inASleep(t, engine))
	require.NoError(t, rig.h.client(t, testVersion).Cancel(testItem))
	rig.h.tick(t)

	outcome := receive(t, done)

	require.Error(t, outcome.err)
	assert.Len(t, rig.merger.landed(), 1, "the run ends in the wait and reads no more")
}

func TestMerge_TheItemGetsItsWorktreeBeforeTheFirstGitCall(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.trees.late = &lateFolder{dir: t.TempDir()}
	rig.host.trees = rig.h.trees

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	assert.NotEmpty(t, rig.host.folderAtGit, "Ensure made the worktree before the host used it")
}

func TestMerge_AnItemWithoutAFolderFailsBeforeAnyGitCallWithAFix(t *testing.T) {
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

func TestMerge_AMemberWhosePullRequestIsNotOpenIsRejectedAndTheRestAreNotMerged(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.notOpen = map[string]bool{"1": true}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err, "a member that was closed by hand is a member problem, and not a failure of the run")
	assert.Contains(t, rig.reportLine(t), "1:the pull request of item 1 is MERGED, not OPEN")
	assert.Empty(t, rig.merger.landed(), "the union was gated with member 1, so member 3 waits for a new gate")
}

func TestMerge_ATransientRefusalDefersTheMemberAndTheMergerRunsOnce(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	failed := merge.Result{Stop: merge.Refused, Reason: merge.Transient, Lines: []string{"first", "REFUSE: MergePR: down"}, Refusal: []string{"REFUSE: MergePR: down"}}
	rig.merger.script["1"] = []merge.Result{failed, failed, failed}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	assert.Contains(t, rig.reportLine(t), "1:REFUSE: MergePR: down")
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the merge step of a member never runs twice in one attempt, and the later member waits")
}

func TestMerge_ADeferredMemberKeepsTheWholeReason(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.script["1"] = []merge.Result{{
		Stop:    merge.Refused,
		Reason:  merge.Transient,
		Lines:   []string{"OK: x", "REFUSE: checks still running: ci. Wait for them.", "       (above refers to #1)"},
		Refusal: []string{"REFUSE: checks still running: ci. Wait for them.", "       (above refers to #1)"},
	}}

	_, err := rig.walk(t, rig.call("1 3", ""))

	require.NoError(t, err)
	assert.Contains(t, rig.reportLine(t), "1:REFUSE: checks still running: ci. Wait for them.\n       (above refers to #1)")
}

func TestMerge_ACancelWhileTheMergeStepRunsThenAResumeMergesTheMemberOnce(t *testing.T) {
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

	_, err := engine.RunFile(testItem, testProject, file, modeOff())

	require.Error(t, err)
	_, err = resumeAndJoin(t, engine, rig.h.client(t, testVersion))

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "1", "3"}, rig.merger.landed(), "the cut-off merge step ran again")
	assert.Equal(t, []string{"1", "3"}, rig.host.intents, "the intent step is kept, so the merge step has the intent of the first run")
	assert.Equal(t, 1, strings.Count(rig.repo.must(t, "log", "--format=%s", "main"), "land 1"), "the member landed once")
	assert.Len(t, rig.gate.ran(), 1, "the union gate ran once")
}

func TestMerge_TheStepNameOfAMemberNamesTheCallTheKindAndTheItem(t *testing.T) {
	t.Parallel()

	b := &batchBase{call: batchCall{name: "n"}}

	assert.Equal(t, "merge:n.intent#bd-a3f8", b.stepName("intent", "bd-a3f8"))
}

func TestMerge_AMergeWithAContextThatIsNotARunIsRefused(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	s := &script{e: rig.engine(t)}

	_, err := (&batchBase{s: s}).mergeReads(t.Context(), workflow.PullRequest{}, MergeSettings{})
	require.ErrorIs(t, err, errNotRunContext)
}

func TestMerge_ACallOutsideAWorkflowRunIsRefused(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	engine := rig.engine(t)
	s := &script{e: engine, ctx: engine.ctx}

	require.ErrorIs(t, s.batchScope("b"), errNoScope)
}

func TestMerge_ATransientFailureOfTheTipReadIsRetriedInsideTheStep(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.tipErrs = []error{errFake}
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.call("1 3", ""))), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	for _, step := range steps {
		assert.NoError(t, step.Error, step.StepName, "no step records the transient failure")
	}
}

func TestMerge_AFailureThatStaysIsRecordedAndAResumeRunsTheStepAgain(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.tipErrs = []error{nil, errFake, errFake, errFake}
	file := writeFlow(t, bodyFlow(rig.call("1 3", "")))
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file, modeOff())

	require.ErrorIs(t, err, errFake)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	var recorded error

	for _, step := range steps {
		if step.StepName == "merge:b.intent#1" {
			recorded = step.Error
		}
	}

	require.Error(t, recorded, "DBOS records the error of a step that ended in one, and a replay returns it without running the step")

	reads := rig.host.tipReads()

	_, err = resumeAndJoin(t, engine, rig.h.client(t, testVersion))

	require.NoError(t, err)
	assert.Greater(t, rig.host.tipReads(), reads, "the resume drops the failed record, so the tip is read again")
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
}
