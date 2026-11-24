package flow

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// secondCallBound is the time that a run needs after the answer to land a member twice. It stays far below the bound of the package, so a lost kept issue fails its test by name.
const secondCallBound = 30 * time.Second

// askBound is the time that a run of the test gets to end before the test takes it as a run that asked nothing.
const askBound = 5 * time.Second

// scopeBody gates the item once, which fixes its scope, then merges the pull request of the item.
func (r *batchRig) scopeBody() string {
	return "gate(name = \"g\")\n" + r.call("1", "")
}

// runItemOne runs the file as item 1, the item of the pull request that the call merges, so the gate of the run fixes the scope of that member.
func (r *batchRig) runItemOne(t *testing.T, engine *Engine) <-chan runOutcome {
	t.Helper()

	done := make(chan runOutcome, 1)
	file := writeFlow(t, bodyFlow(r.scopeBody()))

	go func() {
		result, err := engine.RunFile("1", testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	return done
}

func TestMerge_ARefusedMemberOfABatchOfOneIsRejectedAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	done := rig.runItemOne(t, engine)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "no"}))

	got := receive(t, done)

	require.NoError(t, got.err)
	assert.Empty(t, rig.merger.landed(), "the refused member is not merged")
	require.Len(t, rig.h.commands.ran(), 1)
	assert.Contains(t, rig.h.commands.ran()[0], "the pull request closes an issue that is new since its first review")
}

func TestMerge_AnOwnerLineThatNamesTheNewIssueLetsTheMemberLand(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {13, 12, 13}}
	engine := rig.engine(t)
	done := rig.runItemOne(t, engine)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "keep #13"}))

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestMerge_AMemberWhoseIssuesDidNotGrowAsksNoQuestion(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12, 13}, {12}}

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow(rig.scopeBody())), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestMerge_AMemberWithNoScopeOfItsRunHasNothingToCompare(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}

	_, err := rig.walk(t, rig.call("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "a run with no first review reads no issue and asks nothing")
	assert.Zero(t, rig.h.forge.closingReads, "nothing is read when there is no scope to compare")
}

func TestMerge_AMemberWhoseIssueReadFailsEndsTheCallAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}
	rig.h.forge.closingErr, rig.h.forge.closingOK = errors.New("the forge is down"), 1

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow(rig.scopeBody())), modeOff())

	require.ErrorContains(t, err, "scope check of item 1")
	assert.Empty(t, rig.merger.landed())
}

// fixScopeOf runs a gate as the item, so that the item has an earlier run with a scope for a member check to compare with.
func (r *batchRig) fixScopeOf(t *testing.T, engine *Engine, item string) {
	t.Helper()

	_, err := engine.RunFile(item, testProject, writeFlow(t, bodyFlow("gate(name = \"g\")")), modeOff())
	require.NoError(t, err)
}

// runMergeAs starts a merge of the listed items as a run of runItem, which is not one of the members.
func (r *batchRig) runMergeAs(t *testing.T, engine *Engine, runItem, items string) <-chan runOutcome {
	t.Helper()

	done := make(chan runOutcome, 1)
	file := writeFlow(t, bodyFlow(r.call(items, "")))

	go func() {
		result, err := engine.RunFile(runItem, testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	return done
}

// questionOf waits until the run of the item shows a question and returns it.
func questionOf(t *testing.T, h *harness, item string) string {
	t.Helper()

	client := h.client(t, testVersion)
	read := newStatusRead(client)
	question := ""

	waitOn(t, waiting[struct{}]{what: "the run of item " + item + " to show a question", run: runOfClient(t, client), last: read.last, read: func() (struct{}, bool, error) {
		for _, info := range read.next() {
			if info.Item == item && info.Question != "" {
				question = info.Question

				return struct{}{}, true, nil
			}
		}

		return struct{}{}, false, nil
	}})

	return question
}

func TestMerge_AMemberOfAnotherItemTakesItsBaselineFromTheEarlierRunOfThatMember(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	done := rig.runMergeAs(t, engine, "9", "1")

	question := questionOf(t, rig.h, "9")

	assert.Contains(t, question, "item 1 closes #13 now", "#12 is in the first review of item 1, so only #13 is new")
	assert.NotContains(t, question, "#12 now")
	assert.Contains(t, question, `Answer in the form "keep #N[, #N ...]", for example "keep #13".`, "the question shows the form and an example")
	assert.Contains(t, question, "Answer with beehave answer 9.", "the question names the run whose wait the owner answers")
	require.ErrorContains(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "keep #13"}), "waits for no answer", "the member is not the run that waits")
	require.NoError(t, rig.h.client(t, testVersion).Reply("9", Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestMerge_TheScopeQuestionListsTheIssuesOfTheFirstReviewNextToTheNewIssue(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12, 14}, {12, 13, 14}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	done := rig.runMergeAs(t, engine, "9", "1")

	question := questionOf(t, rig.h, "9")

	assert.Contains(t, question, "closes #13 now, and did not at its first review (then: #12 #14).", "the owner sees the issues that the new one is compared against")
	require.NoError(t, rig.h.client(t, testVersion).Reply("9", Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, done).err)
}

func TestMerge_TheIssuesTheOwnerKeptGoToTheMergeAsKept(t *testing.T) {
	t.Parallel()

	rig, _ := answerTheScopeQuestion(t, [][]int{{12}, {12, 13}}, "keep #13")

	require.Equal(t, []string{"1"}, rig.merger.landed())
	require.Len(t, rig.merger.given, 1)
	assert.Equal(t, []model.IssueRef{"#13"}, rig.merger.given[0].Kept, "the closing keyword guard of the merge may name the kept issue")
}

func TestMerge_ASecondMergeCallOfAMemberInTheSameRunAsksNoSecondQuestionForAKeptIssue(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")

	file := writeFlow(t, bodyFlow(rig.call("1", "")+strings.Replace(rig.call("1", ""), `name = "b"`, `name = "c"`, 1)))
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile("9", testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	questionOf(t, rig.h, "9")
	require.NoError(t, rig.h.client(t, testVersion).Reply("9", Reply{Text: "keep #13"}))

	select {
	case outcome := <-done:
		require.NoError(t, outcome.err)
	case <-time.After(secondCallBound):
		t.Fatalf("the run of item 9 did not end within %s after the answer: the second call of the member asked a second question, so the kept issue was lost", secondCallBound)
	}

	assert.Equal(t, []string{"1", "1"}, rig.merger.landed(), "the kept issue needs no second answer, so the second call lands the member")
	require.Len(t, rig.merger.given, 2)
	assert.Equal(t, []model.IssueRef{"#13"}, rig.merger.given[1].Kept)
}

func TestMerge_AMemberOfAnotherItemWithAnEarlierRunWhoseIssuesDidNotGrowAsksNoQuestion(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12, 13}, {12}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")

	_, err := engine.RunFile("9", testProject, writeFlow(t, bodyFlow(rig.call("1", ""))), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Equal(t, 2, rig.h.forge.closingReads, "one read for the first review of item 1 and one for the check before its merge")
}

func TestMerge_AMemberOfAnotherItemWithNoEarlierRunHasNothingToCompare(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}

	_, err := engineRunAs(t, rig, "9", "1")

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Zero(t, rig.h.forge.closingReads, "a member with no first review reads no issue and asks nothing")
}

func engineRunAs(t *testing.T, rig *batchRig, runItem, items string) (ItemResult, error) {
	t.Helper()

	return rig.engine(t).RunFile(runItem, testProject, writeFlow(t, bodyFlow(rig.call(items, ""))), modeOff())
}

func TestMerge_AMemberWhoseOwnerAnswerIsNoIsRejectedAndTheOtherMembersLand(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {20}, {12, 13}, {20}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	rig.fixScopeOf(t, engine, "3")
	done := rig.runMergeAs(t, engine, "9", "1 3")

	questionOf(t, rig.h, "9")
	require.NoError(t, rig.h.client(t, testVersion).Reply("9", Reply{Text: "no"}))
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the refused member is rejected and the other member lands")
	require.Len(t, rig.h.commands.ran(), 1)
	report := rig.h.commands.ran()[0]
	assert.Contains(t, report, "1:", "item 1 is in the rejected list")
	assert.Contains(t, report, "beehave answer 9 ", "the refusal names the run whose wait the owner answers")
	assert.NotContains(t, report, "beehave answer 1 ")
}

// answerTheScopeQuestion gates item 1 once, runs a merge of item 1 as a run of item 1 over the closing sets, answers its one question, and returns the rig and the reason of the report.
func answerTheScopeQuestion(t *testing.T, closing [][]int, answer string) (*batchRig, string) {
	t.Helper()

	rig := newBatchRig(t)
	rig.h.forge.closing = closing
	engine := rig.engine(t)
	done := rig.runItemOne(t, engine)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: answer}))
	require.NoError(t, receive(t, done).err)

	if ran := rig.h.commands.ran(); len(ran) > 0 {
		return rig, ran[0]
	}

	return rig, ""
}

func TestMerge_AnAnswerThatIsNotTheKeepFormRejectsTheMemberAndShowsTheForm(t *testing.T) {
	t.Parallel()

	for _, answer := range []string{"keep #130", "no, do not keep #13", "stop #13", "13", "keep 13 please", "keep"} {
		rig, report := answerTheScopeQuestion(t, [][]int{{12}, {12, 13}}, answer)

		assert.Empty(t, rig.merger.landed(), answer)
		assert.Contains(t, report, "#13 has no owner line", answer)
		assert.Contains(t, report, "\"keep #13\"", answer)
	}
}

func TestMerge_TheKeepFormIsCaseInsensitiveAndTakesAList(t *testing.T) {
	t.Parallel()

	rig, _ := answerTheScopeQuestion(t, [][]int{{12}, {12, 13, 14}}, " KEEP #13, #14 ")

	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestMerge_AnAnswerThatNamesOneOfTwoNewIssuesRejectsTheMemberAndNamesTheOther(t *testing.T) {
	t.Parallel()

	rig, report := answerTheScopeQuestion(t, [][]int{{12}, {12, 13, 14}}, "keep #13")

	assert.Empty(t, rig.merger.landed())
	assert.Contains(t, report, "#14 has no owner line")
	assert.NotContains(t, report, "#13 has")
}

func TestMerge_ASetThatChangesAfterTheFirstGateIsComparedWithTheFirstGate(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}, {12, 13}}
	engine := rig.engine(t)
	done := make(chan runOutcome, 1)
	file := writeFlow(t, bodyFlow("gate(name = \"first\")\ngate(name = \"second\")\n"+rig.call("1", "")))

	go func() {
		result, err := engine.RunFile("1", testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	question := questionOf(t, rig.h, "1")
	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "no"}))
	require.NoError(t, receive(t, done).err)

	assert.Contains(t, question, "item 1 closes #13 now", "the second gate reads nothing, so #13 is new against the first gate")
	assert.Empty(t, rig.merger.landed())
	assert.Equal(t, 2, rig.h.forge.closingReads, "the first gate and the check before the merge; the second gate reads nothing")
}

func TestMerge_ANewRunOfTheMemberKeepsTheScopeOfItsFirstReview(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	done := rig.runItemOne(t, engine)

	questionOf(t, rig.h, "1")
	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the second run compares with the first review of the first run")
	assert.Equal(t, 2, rig.h.forge.closingReads, "the second gate reads nothing: an earlier run fixed the scope")
}

func TestMerge_ANewRunOfTheMemberWhoseEarlierRunFixedNoScopeReadsTheForgeAtItsGate(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}
	engine := rig.engine(t)

	_, err := engine.RunFile("1", testProject, writeFlow(t, bodyFlow("pass\n")), modeOff())
	require.NoError(t, err)

	_, err = engine.RunFile("1", testProject, writeFlow(t, bodyFlow(rig.scopeBody())), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Equal(t, 2, rig.h.forge.closingReads, "one read at the gate and one before the merge")
}

func TestMerge_AMemberWhoseNameEndsInAnAttemptMarkerLendsNoScopeToTheShorterName(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{5}, {7}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1-r2")

	_, err := engine.RunFile("9", testProject, writeFlow(t, bodyFlow(rig.call("1", ""))), modeOff())

	require.NoError(t, err, "member 1 has no first review of its own, so nothing is compared and nothing is asked")
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestMerge_AGateBeforeThePullRequestExistsFixesNoScope(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.noPull = true

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow("gate(name = \"g\")\n")), modeOff())

	require.NoError(t, err)
	assert.Equal(t, 1, rig.h.forge.closingReads)
}

func TestMerge_AFailedReadAtTheFirstGateEndsTheItem(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closingErr = errFake

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow("gate(name = \"g\")\n")), modeOff())

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "scope of item 1")
	require.ErrorContains(t, err, errFake.Error())
}

func TestMerge_AFailedReadBeforeTheMergeOfAMemberEndsTheCallAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}
	rig.h.forge.closingErr, rig.h.forge.closingOK = errFake, 1

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow(rig.scopeBody())), modeOff())

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "scope check of item 1")
	assert.Empty(t, rig.merger.landed())
}

func TestMerge_AMemberWhoseGateFixedNoScopeLeavesTheMergeToReadNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.noPull = true

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow(rig.scopeBody())), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Equal(t, 1, rig.h.forge.closingReads, "only the read of the gate: the merge has no first review to compare")
}

func TestMerge_ANewRunOfAMemberWithTheSameIssuesAsksNoQuestion(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")

	_, err := engine.RunFile("1", testProject, writeFlow(t, bodyFlow(rig.scopeBody())), modeOff())

	require.NoError(t, err, "the second run asks nothing: its issues equal the first review")
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestMerge_AMemberWhosePullRequestIsGoneBeforeItsMergeEndsTheCallAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}
	rig.h.forge.noPull, rig.h.forge.noPullAfter = true, 1

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow(rig.scopeBody())), modeOff())

	require.ErrorContains(t, err, "the item has no pull request")
	assert.Empty(t, rig.merger.landed())
}

func TestMerge_ARunThatIsCanceledWhileItAsksTheOwnerEndsTheCallAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	done := rig.runItemOne(t, engine)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Cancel("1"))
	rig.h.tick(t)

	require.Error(t, receive(t, done).err)
	assert.Empty(t, rig.merger.landed())
}

func TestScope_TheEarlierRunsOfAnItemComeNewestAttemptFirstAndNumericallyAndWithoutTheRunItself(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	h.flowIs(t, bodyFlow("pass\n"))

	for range 10 {
		_, err := engine.Run(testItem, testProject)
		require.NoError(t, err)
	}

	// The engine context is outside a workflow, so no run is excluded as the current one.
	ids, err := (&script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}).earlierRuns(testItem)
	require.NoError(t, err)

	want := []string{}
	for attempt := 10; attempt >= 1; attempt-- {
		want = append(want, runID(testItem, attempt))
	}

	assert.Equal(t, want, ids, "attempt 10 comes before attempt 9, which a text sort would not give")
}

func TestScope_AStoreThatIsShutDownRefusesTheReadOfAnEarlierScope(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, err = (&script{e: engine, in: ItemInput{Item: testItem}}).earlierScope(testItem)
	require.ErrorContains(t, err, "list the earlier runs of item 12")

	_, err = recordedScope(engine.ctx, "item-12-r1")
	require.ErrorContains(t, err, "read the steps of item-12-r1")
}

func TestScope_AStoreThatIsShutDownRefusesTheReadOfTheKeptIssues(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, err = (&script{e: engine, in: ItemInput{Item: testItem}}).keptOf("1")
	require.ErrorContains(t, err, "list the earlier runs of item 12")

	err = (&batchBase{s: &script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}}).keep("1", []model.IssueRef{"#13"})
	require.ErrorContains(t, err, "record the issues kept for item 1")
}

func TestScope_ARunWhoseStepsTheStoreCannotReadRefusesTheReadOfTheKeptIssues(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{Forge: &fakeForge{closing: [][]int{{12}}}})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, err = recordedKept(engine.ctx, "item-12-r1", "1")
	require.ErrorContains(t, err, "read the steps of item-12-r1")

	_, err = (&script{e: engine, ctx: engine.ctx, fixed: true, in: ItemInput{Item: testItem, Project: testProject}}).freshIssues(t.Context(), testItem)
	require.ErrorContains(t, err, "list the earlier runs of item 12")
}

func TestKeptIssues_ReadsOnlyTheKeepForm(t *testing.T) {
	t.Parallel()

	kept, ok := keptIssues("keep #13, #14")
	assert.True(t, ok)
	assert.Equal(t, []model.IssueRef{"#13", "#14"}, kept)

	kept, ok = keptIssues("Keep #13, Other-Org/Other-Repo#5")
	assert.True(t, ok)
	assert.Equal(t, []model.IssueRef{"#13", "other-org/other-repo#5"}, kept, "an issue of another repository is kept by its full reference, in any case")

	for _, text := range []string{"keep #99999999999999999999", "keep other/repo#99999999999999999999", "keep 5", "keep other/repo", "keep other-org/other-repo#5 and #6"} {
		_, ok = keptIssues(text)
		assert.False(t, ok, text)
	}
}

func TestIssueList_NamesEachIssueOrNoIssue(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "no issue", issueList(nil))
	assert.Equal(t, "#12 #13", issueList([]model.IssueRef{"#12", "#13"}))
	assert.Equal(t, "#12 other-org/other-repo#5", issueList([]model.IssueRef{"#12", "other-org/other-repo#5"}))
}

func TestMerge_ALaterGateOfTheRunKeepsTheScopeOfTheFirstAndReadsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow("gate(name = \"g\")\ngate(name = \"h\")\ngate(name = \"i\")\n"+rig.call("1", ""))), modeOff())

	require.NoError(t, err)
	assert.Equal(t, 2, rig.h.forge.closingReads, "one read at the first gate and one before the merge")
}

// keepAs runs a merge of the members as a run of runItem, answers its one question with "keep #13", and returns once the run ends.
func (r *batchRig) keepAs(t *testing.T, engine *Engine, runItem, members string) {
	t.Helper()

	done := r.runMergeAs(t, engine, runItem, members)

	questionOf(t, r.h, runItem)
	require.NoError(t, r.h.client(t, testVersion).Reply(runItem, Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, done).err)
}

func TestMerge_ANewRunOfTheSameItemAsksNoSecondQuestionForAnIssueTheOwnerKept(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	rig.keepAs(t, engine, "9", "1")

	done := rig.runMergeAs(t, engine, "9", "1")

	select {
	case outcome := <-done:
		require.NoError(t, outcome.err)
	case <-time.After(secondCallBound):
		t.Fatalf("the second run of item 9 did not end within %s: it asked the owner again about the issue that run one kept", secondCallBound)
	}

	assert.Equal(t, []string{"1", "1"}, rig.merger.landed())
	require.Len(t, rig.merger.given, 2)
	assert.Equal(t, []model.IssueRef{"#13"}, rig.merger.given[1].Kept, "the merge of run two still gets the kept issue")
}

func TestMerge_AnIssueKeptByARunOfOneItemIsStillAskedAboutByARunOfAnotherItem(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	rig.keepAs(t, engine, "9", "1")

	rig.keepAs(t, engine, "8", "1")

	assert.Equal(t, []string{"1", "1"}, rig.merger.landed(), "run 8 asked its own question and landed the member after the answer")
}

func TestMerge_AnIssueKeptForOneMemberIsStillAskedAboutForAnotherMemberOfTheSameItem(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closingOf = map[string][][]int{"1": {{12}, {12, 13}}, "3": {{12}, {12, 13}}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	rig.fixScopeOf(t, engine, "3")
	rig.keepAs(t, engine, "9", "1")

	done := rig.runMergeAs(t, engine, "9", "3")

	select {
	case <-done:
		t.Fatal("the run of item 9 ended without a question: the keep of #13 for member 1 covered member 3")
	case <-time.After(askBound):
	}

	questionOf(t, rig.h, "9")
	require.NoError(t, rig.h.client(t, testVersion).Reply("9", Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed(), "member 3 asked its own question and landed after the answer")
}

func TestMerge_AnIssueKeptInRunOneStaysKeptWhenThePullRequestOfRunTwoDropsIt(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closingOf = map[string][][]int{"1": {{12}, {12, 13}, {12}}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	rig.keepAs(t, engine, "9", "1")

	done := rig.runMergeAs(t, engine, "9", "1")

	select {
	case outcome := <-done:
		require.NoError(t, outcome.err)
	case <-time.After(secondCallBound):
		t.Fatalf("the second run of item 9 did not end within %s", secondCallBound)
	}

	require.Len(t, rig.merger.given, 2)
	assert.Equal(t, []model.IssueRef{"#13"}, rig.merger.given[1].Kept, "the kept set grows only: the guard allows #13 although the pull request no longer closes it")
}
