package flow

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgepkg "github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

// The tests of this file run the library scope.star on the merge rig: the closing sets come from the fake forge, the owner answers through the client, and the fake merger records what it gets.

// secondCallBound is the time that a run needs after the answer to land a member twice. It stays far below the bound of the package, so a lost kept issue fails its test by name.
const secondCallBound = 30 * time.Second

// askBound is the time that a run of the test gets to end before the test takes it as a run that asked nothing.
const askBound = 5 * time.Second

// scopeOffNote is the note that the status of a run shows when the project has no forge.
const scopeOffNote = "scope check off: the project has no forge"

const (
	fixFirstReview = `fix_scope(name = "scope")`
	scopeLoad      = "load(\"@beehave//scope.star\", \"fix_scope\", \"hold_scope\")\n\n"
	otherIssue     = model.IssueRef("other-org/other-repo#5")
)

// libFlow is a workflow file with the statements pre, then holds rounds of hold_scope over the items and a merge of the members that may land, then a report of the merged items and the rejected ones.
func (r *batchRig) libFlow(pre, items string, holds int) string {
	var members []string

	for _, item := range strings.Fields(items) {
		members = append(members, `struct(item = "`+item+`", head = "`+r.heads[item]+`", closes = [])`)
	}

	body := []string{"def main(item):", "    " + pre, "    merged = []", "    rejected = []"}

	for k := 1; k <= holds; k++ {
		n := string(rune('0' + k))
		body = append(body,
			`    held = hold_scope(name = "hold-`+n+`", prs = [`+strings.Join(members, ", ")+`])`,
			"    rejected += held.rejected",
			"    if held.prs:",
			`        merged += merge(prs = held.prs, base = "main", repo = "o/r", name = "b-`+n+`").merged`)
	}

	body = append(body, `    command(name = "report", run = ["report", ",".join(merged), ",".join([r.item + ":" + r.reason for r in rejected]), ""])`)

	return scopeLoad + strings.Join(body, "\n") + "\n"
}

// scopeBody fixes the scope at the first review of the run item, then checks and merges the pull request of the item.
func (r *batchRig) scopeBody() string { return r.libFlow(fixFirstReview, "1", 1) }

// runFlowAs starts the workflow text as a run of the item.
func (r *batchRig) runFlowAs(t *testing.T, engine *Engine, runItem, text string) <-chan runOutcome {
	t.Helper()

	done := make(chan runOutcome, 1)
	file := writeFlow(t, text)

	go func() {
		result, err := engine.RunFile(runItem, testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	return done
}

// runItemOne runs the file as item 1, the item of the pull request that the call merges, so the first review of the run fixes the scope of that member.
func (r *batchRig) runItemOne(t *testing.T, engine *Engine) <-chan runOutcome {
	t.Helper()

	return r.runFlowAs(t, engine, "1", r.scopeBody())
}

// fixScopeOf runs fix_scope as the item, so that the item has an earlier run with a scope for a member check to compare with.
func (r *batchRig) fixScopeOf(t *testing.T, engine *Engine, item string) {
	t.Helper()

	_, err := engine.RunFile(item, testProject, writeFlow(t, scopeLoad+"def main(item):\n    "+fixFirstReview+"\n"), modeOff())
	require.NoError(t, err)
}

// runMergeAs starts a check and merge of the listed items as a run of runItem, which is not one of the members.
func (r *batchRig) runMergeAs(t *testing.T, engine *Engine, runItem, items string) <-chan runOutcome {
	t.Helper()

	return r.runFlowAs(t, engine, runItem, withRun(r.libFlow("pass", items, 1), runItem))
}

// withRun names the item of the run in each hold_scope call of the workflow text, as the call of a round does.
func withRun(text, runItem string) string {
	return strings.ReplaceAll(text, `prs = [`, `run = "`+runItem+`", prs = [`)
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

// answerTheScopeQuestion runs item 1 over the closing sets, answers its one question, and returns the rig and the report of the run.
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

// keepAs runs a check and merge of the members as a run of runItem, answers its one question with "keep #13", and returns once the run ends.
func (r *batchRig) keepAs(t *testing.T, engine *Engine, runItem, members string) {
	t.Helper()

	done := r.runMergeAs(t, engine, runItem, members)

	questionOf(t, r.h, runItem)
	require.NoError(t, r.h.client(t, testVersion).Reply(runItem, Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, done).err)
}

func TestHoldScope_AMemberTheOwnerRefusesIsRejectedAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig, report := answerTheScopeQuestion(t, [][]int{{12}, {12, 13}}, "no")

	assert.Empty(t, rig.merger.landed(), "the refused member is not merged")
	assert.Contains(t, report, "the pull request closes an issue that is new since its first review")
}

func TestHoldScope_AnOwnerLineThatNamesTheNewIssueLetsTheMemberLandWithTheKeptIssueAndTheCheckedSet(t *testing.T) {
	t.Parallel()

	rig, _ := answerTheScopeQuestion(t, [][]int{{12}, {13, 12, 13}}, "keep #13")

	require.Equal(t, []string{"1"}, rig.merger.landed())
	require.Len(t, rig.merger.given, 1)
	assert.Equal(t, []model.IssueRef{"#13"}, rig.merger.given[0].Kept, "the closing keyword guard of the merge may name the kept issue")
	assert.Equal(t, []model.IssueRef{"#12", "#13"}, rig.merger.given[0].Closing, "the merge gets the set that the check read, sorted and without a repeat")
}

func TestHoldScope_AMemberWhoseIssuesDidNotGrowAsksNoQuestion(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12, 13}, {12}}

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, rig.scopeBody()), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Equal(t, 2, rig.h.forge.closingReads, "one read fixes the scope and one checks the member")
}

func TestHoldScope_AMemberWithNoFirstReviewHasNothingToCompareAndTheCheckReadsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}

	_, err := rig.engine(t).RunFile("9", testProject, writeFlow(t, rig.libFlow("pass", "1", 1)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "a member of another item has no first review and asks nothing")
	assert.Zero(t, rig.h.forge.closingReads, "a member with no first review is not read")
	require.Len(t, rig.merger.given, 1)
	assert.Empty(t, rig.merger.given[0].Kept)
	assert.Nil(t, rig.merger.given[0].Closing, "a member with no first review pins no set")
}

func TestHoldScope_AMemberWhoseIssueReadFailsEndsTheRunAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}
	rig.h.forge.closingErr, rig.h.forge.closingOK = errFake, 1

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, rig.scopeBody()), modeOff())

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the issues that the pull request of item 1 closes")
	assert.Empty(t, rig.merger.landed())
}

func TestHoldScope_AMemberWhosePullRequestIsGoneBeforeItsCheckEndsTheRunAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}
	rig.h.forge.noPull, rig.h.forge.noPullAfter = true, 1

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, rig.scopeBody()), modeOff())

	require.ErrorContains(t, err, "the item has no pull request")
	assert.Empty(t, rig.merger.landed())
}

func TestHoldScope_AMemberOfAnotherItemTakesItsBaselineFromTheEarlierRunOfThatMember(t *testing.T) {
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
	assert.Contains(t, question, "Answer with beehave answer 9.", "the question names the run that the call gives")
	require.NoError(t, rig.h.client(t, testVersion).Reply("9", Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestHoldScope_TheQuestionNamesTheRunThatTheCallGivesAndListsTheIssuesOfTheFirstReview(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12, 14}, {12, 13, 14}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	done := rig.runMergeAs(t, engine, "9", "1")

	question := questionOf(t, rig.h, "9")

	assert.Contains(t, question, "closes #13 now, and did not at its first review (then: #12 #14).", "the owner sees the issues that the new one is compared against")
	assert.Contains(t, question, "Answer with beehave answer 9.", "the question names the run whose wait the owner answers")
	require.ErrorContains(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "keep #13"}), "waits for no answer", "the member is not the run that waits")
	require.NoError(t, rig.h.client(t, testVersion).Reply("9", Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, done).err)
}

func TestHoldScope_ASecondCheckOfAMemberInTheSameRunAsksNoSecondQuestionForAKeptIssue(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	done := rig.runFlowAs(t, engine, "9", withRun(rig.libFlow("pass", "1", 2), "9"))

	questionOf(t, rig.h, "9")
	require.NoError(t, rig.h.client(t, testVersion).Reply("9", Reply{Text: "keep #13"}))

	select {
	case outcome := <-done:
		require.NoError(t, outcome.err)
	case <-time.After(secondCallBound):
		t.Fatalf("the run did not end within %s after the answer: the second check of the member asked a second question, so the kept issue was lost", secondCallBound)
	}

	assert.Equal(t, []string{"1", "1"}, rig.merger.landed(), "the kept issue needs no second answer, so the second check lands the member")
	require.Len(t, rig.merger.given, 2)
	assert.Equal(t, []model.IssueRef{"#13"}, rig.merger.given[1].Kept)
}

func TestHoldScope_ANewRunOfTheSameItemAsksNoSecondQuestionForAnIssueTheOwnerKept(t *testing.T) {
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

func TestHoldScope_AnIssueKeptByARunOfOneItemIsStillAskedAboutByARunOfAnotherItem(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	rig.keepAs(t, engine, "9", "1")

	rig.keepAs(t, engine, "8", "1")

	assert.Equal(t, []string{"1", "1"}, rig.merger.landed(), "run 8 asked its own question and landed the member after the answer")
}

func TestHoldScope_AnIssueKeptForOneMemberIsStillAskedAboutForAnotherMemberOfTheSameRunItem(t *testing.T) {
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

func TestHoldScope_AnIssueKeptInRunOneStaysKeptWhenThePullRequestOfRunTwoDropsIt(t *testing.T) {
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

func TestHoldScope_AMemberWhoseOwnerAnswerIsNoIsRejectedAndTheOtherMembersLand(t *testing.T) {
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

func TestHoldScope_AnAnswerThatIsNotTheKeepFormRejectsTheMemberAndShowsTheForm(t *testing.T) {
	t.Parallel()

	for _, answer := range []string{"keep #130", "no, do not keep #13", "stop #13", "13", "keep 13 please", "keep"} {
		rig, report := answerTheScopeQuestion(t, [][]int{{12}, {12, 13}}, answer)

		assert.Empty(t, rig.merger.landed(), answer)
		assert.Contains(t, report, "#13 has no owner line", answer)
		assert.Contains(t, report, "\"keep #13\"", answer)
	}
}

func TestHoldScope_TheKeepFormIsCaseInsensitiveAndTakesAList(t *testing.T) {
	t.Parallel()

	rig, _ := answerTheScopeQuestion(t, [][]int{{12}, {12, 13, 14}}, " KEEP #13, #14 ")

	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestHoldScope_AnAnswerThatNamesOneOfTwoNewIssuesRejectsTheMemberAndNamesTheOther(t *testing.T) {
	t.Parallel()

	rig, report := answerTheScopeQuestion(t, [][]int{{12}, {12, 13, 14}}, "keep #13")

	assert.Empty(t, rig.merger.landed())
	assert.Contains(t, report, "#14 has no owner line")
	assert.NotContains(t, report, "#13 has")
}

func TestFixScope_ALaterReadOfTheRunKeepsTheScopeOfTheFirstAndReadsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}, {12, 13}}
	engine := rig.engine(t)
	done := rig.runFlowAs(t, engine, "1", rig.libFlow(fixFirstReview+"\n    "+fixFirstReview, "1", 1))

	question := questionOf(t, rig.h, "1")
	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "no"}))
	require.NoError(t, receive(t, done).err)

	assert.Contains(t, question, "item 1 closes #13 now", "the first review holds #12, so #13 is new")
	assert.Empty(t, rig.merger.landed())
	assert.Equal(t, 2, rig.h.forge.closingReads, "the first fix and the check before the merge; the second fix reads nothing")
}

func TestFixScope_ANewRunOfTheMemberKeepsTheScopeOfItsFirstReviewAndReadsNothing(t *testing.T) {
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
	assert.Equal(t, 2, rig.h.forge.closingReads, "the fix of the second run reads nothing: an earlier run fixed the scope")
}

func TestFixScope_ANewRunOfTheMemberWhoseEarlierRunFixedNoScopeReadsTheForge(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}
	engine := rig.engine(t)

	_, err := engine.RunFile("1", testProject, writeFlow(t, bodyFlow("pass\n")), modeOff())
	require.NoError(t, err)

	_, err = engine.RunFile("1", testProject, writeFlow(t, rig.scopeBody()), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Equal(t, 2, rig.h.forge.closingReads, "one read at the fix and one for the check before the merge")
}

func TestFixScope_AReadBeforeThePullRequestExistsFixesNoScope(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.noPull = true

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, rig.scopeBody()), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "no pull request at the first review means no first review to compare with")
	assert.Equal(t, 1, rig.h.forge.closingReads, "the check reads nothing for a member with no first review")
}

func TestFixScope_AFailedReadEndsTheRun(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closingErr = errFake

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, rig.scopeBody()), modeOff())

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the issues that the pull request of item 1 closes")
}

func TestFixScope_AProjectWithNoForgeShowsInTheRunStatusThatTheScopeCheckIsOff(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closingErr = forgepkg.ErrNoForge
	engine := rig.engine(t)

	_, err := engine.RunFile("1", testProject, writeFlow(t, rig.scopeBody()), modeOff())
	require.NoError(t, err)

	infos, err := rig.h.client(t, testVersion).Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Contains(t, infos[0].Notes(), scopeOffNote)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "a project with no forge has no scope check, so the member lands")

	steps, err := rig.h.client(t, testVersion).Steps(infos[0].ID)
	require.NoError(t, err)
	assert.Contains(t, steps.Notes(), scopeOffNote)
}

func TestFixScope_TheResultTellsTheWorkflowWhetherTheScopeCheckIsOff(t *testing.T) {
	t.Parallel()

	for noForge, want := range map[bool]string{true: "report True", false: "report False"} {
		rig := newBatchRig(t)
		if noForge {
			rig.h.forge.closingErr = forgepkg.ErrNoForge
		}

		text := scopeLoad + "def main(item):\n    s = fix_scope(name = \"scope\")\n    command(name = \"report\", run = [\"report\", str(s.off)])\n"
		_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, text), modeOff())

		require.NoError(t, err)
		assert.Equal(t, []string{want}, rig.h.commands.ran(), "no forge is %t", noForge)
	}
}

func TestFixScope_AProjectWithAForgeShowsNoScopeOffNote(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, rig.scopeBody()), modeOff())
	require.NoError(t, err)

	infos, err := rig.h.client(t, testVersion).Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.NotContains(t, infos[0].Notes(), scopeOffNote)
}

func TestRunProgress_AScopeThatIsOffIsOneNote(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{scopeOffNote}, RunProgress{ScopeOff: true}.Notes())
	assert.Empty(t, RunProgress{}.Notes())
}

func TestHoldScope_ARunThatIsCanceledWhileItAsksTheOwnerEndsTheCallAndLandsNothing(t *testing.T) {
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

// askedAboutTheOtherIssue runs item 1 with a closing issue of another repository at the check, and returns the rig and the question that the run shows.
func askedAboutTheOtherIssue(t *testing.T) (*batchRig, <-chan runOutcome, string) {
	t.Helper()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12}}
	rig.h.forge.closingFar = [][]model.IssueRef{nil, {otherIssue}}
	done := rig.runItemOne(t, rig.engine(t))

	waitOn(t, ownerAsks(t, rig.h))

	infos := newStatusRead(rig.h.client(t, testVersion)).next()
	require.Len(t, infos, 1)

	return rig, done, infos[0].Question
}

func TestHoldScope_AMemberWhoseBodyClosesAnIssueOfAnotherRepositoryMakesTheRunAskTheOwner(t *testing.T) {
	t.Parallel()

	rig, done, question := askedAboutTheOtherIssue(t)

	assert.Contains(t, question, "closes "+string(otherIssue)+" now")
	assert.Contains(t, question, "(then: #12)")
	assert.Contains(t, question, `for example "keep `+string(otherIssue)+`"`)

	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "keep other-org/other-repo#5"}))
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, []string{"1"}, rig.merger.landed())
	require.Len(t, rig.merger.given, 1)
	assert.Equal(t, []model.IssueRef{otherIssue}, rig.merger.given[0].Kept, "the merge guard may name the kept issue of another repository")
	assert.Equal(t, []model.IssueRef{"#12", otherIssue}, rig.merger.given[0].Closing)
}

func TestHoldScope_TheNumberOfAnIssueOfAnotherRepositoryDoesNotKeepIt(t *testing.T) {
	t.Parallel()

	rig, done, _ := askedAboutTheOtherIssue(t)

	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "keep #5"}))
	require.NoError(t, receive(t, done).err)

	assert.Empty(t, rig.merger.landed(), "#5 is an issue of this repository, not other-org/other-repo#5")
	require.Len(t, rig.h.commands.ran(), 1)
	assert.Contains(t, rig.h.commands.ran()[0], string(otherIssue)+" has no owner line")
	assert.Contains(t, rig.h.commands.ran()[0], `"keep `+string(otherIssue)+`"`)
}

func TestHoldScope_AnIssueOfAnotherRepositoryThatTheFirstReviewHeldAsksNoQuestion(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12}}
	rig.h.forge.closingFar = [][]model.IssueRef{{otherIssue}, {otherIssue}}

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, rig.scopeBody()), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestHoldScope_AnIssueOfAnotherRepositoryWithTheNumberOfAnOwnIssueOfTheFirstReviewIsNew(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{5}, {5}}
	rig.h.forge.closingFar = [][]model.IssueRef{nil, {otherIssue}}
	done := rig.runItemOne(t, rig.engine(t))

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "no"}))
	require.NoError(t, receive(t, done).err)

	assert.Empty(t, rig.merger.landed(), "#5 of this repository does not cover other-org/other-repo#5")
}

func TestKeepIssues_ReadsOnlyTheKeepForm(t *testing.T) {
	t.Parallel()

	cases := []struct{ text, want string }{
		{"keep #13, #14", "#13,#14"},
		{"  KEEP   #13 ,#14  ", "#13,#14"},
		{"Keep #13, Other-Org/Other-Repo#5", "#13,other-org/other-repo#5"},
		{"keep #007", "#7"},
		{"keep #9223372036854775807", "#9223372036854775807"},
		{"keep #9223372036854775808", ""},
		{"keep #99999999999999999999", ""},
		{"keep other/repo#99999999999999999999", ""},
		{"keep 5", ""},
		{"keep", ""},
		{"keep#13", ""},
		{"keep #13,", ""},
		{"keep #", ""},
		{"keep #1x", ""},
		{"keep other/repo", ""},
		{"keep other/repo/more#5", ""},
		{"keep /repo#5", ""},
		{"keep o wner/repo#5", ""},
		{"keep other-org/other-repo#5 and #6", ""},
		{"no keep #13", ""},
		{"", ""},
	}

	lines := []string{`load("@beehave//scope.star", "keep_issues")`, "", "def main(item):"}

	for i, c := range cases {
		lines = append(lines, `    command(name = "case-`+strings.Repeat("a", i+1)+`", run = ["seen", ",".join(keep_issues("`+strings.ReplaceAll(c.text, `"`, `\"`)+`"))])`)
	}

	h := newHarness(t)
	_, err := walkText(t, h, strings.Join(lines, "\n")+"\n")
	require.NoError(t, err)

	ran := h.commands.ran()
	require.Len(t, ran, len(cases))

	for i, c := range cases {
		assert.Equal(t, strings.TrimSpace("seen "+c.want), strings.TrimSpace(ran[i]), "the text %q", c.text)
	}
}
