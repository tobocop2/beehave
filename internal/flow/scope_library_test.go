package flow

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgepkg "github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/workflow"
)

// The tests of this file run the library scope.star on the merge rig: the closing sets come from the fake forge, the owner answers through the client, and the fake merger records what it gets.

// scopeOffNote is the note that the status of a run shows when the project has no forge.
const scopeOffNote = "scope check off: the project has no forge"

const (
	fixFirstReview = `own = fix_scope(name = "scope")`
	scopeLoad      = "load(\"@beehest//scope.star\", \"fix_scope\", \"hold_scope\")\n\n"
	otherIssue     = model.IssueRef("other-org/other-repo#5")
)

// libFlow is a workflow file with the statements pre, then holds rounds of hold_scope over the items and a merge of the members that may land, then a report of the merged items and the rejected ones.
func (r *batchRig) libFlow(pre, items string, holds int) string {
	var members []string

	for _, item := range strings.Fields(items) {
		members = append(members, `struct(item = "`+item+`", head = "`+r.heads[item]+`", closes = [])`)
	}

	body := []string{"def main(item):", "    " + pre, "    merged = []", "    rejected = []"}

	given := ""
	if strings.Contains(pre, "own = ") {
		given = "own = own, "
	}

	for k := 1; k <= holds; k++ {
		n := string(rune('0' + k))
		body = append(body,
			`    held = hold_scope(name = "hold-`+n+`", `+given+`prs = [`+strings.Join(members, ", ")+`])`,
			"    rejected += held.rejected",
			"    if held.prs:",
			`        merged += merge(members = held.prs, base = "main", repo = "o/r", name = "b-`+n+`").merged`)
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
	assert.Contains(t, question, "Answer with beehest answer 9.", "the question names the run that the call gives")
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
	assert.Contains(t, question, "Answer with beehest answer 9.", "the question names the run whose wait the owner answers")
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
	answerTaken(t, rig.h, "9")

	endsWithNoQuestion(t, rig.h, "9", done, "the second check of the member asked a second question, so the kept issue was lost")

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

	endsWithNoQuestion(t, rig.h, "9", rig.runMergeAs(t, engine, "9", "1"), "the second run of item 9 asked the owner again about the issue that run one kept")

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

	endsWithNoQuestion(t, rig.h, "9", rig.runMergeAs(t, engine, "9", "1"), "the second run of item 9 asked the owner about an issue that run one kept")

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
	assert.Contains(t, report, "beehest answer 9 ", "the refusal names the run whose wait the owner answers")
	assert.NotContains(t, report, "beehest answer 1 ")
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

// answerTaken waits until the status of the run of the item shows no question, so a later read does not take the question that the test answered for a new one.
func answerTaken(t *testing.T, h *harness, item string) {
	t.Helper()

	client := h.client(t, testVersion)
	read := newStatusRead(client)

	waitOn(t, waiting[struct{}]{what: "the run of item " + item + " to take the answer", run: runOfClient(t, client), last: read.last, read: func() (struct{}, bool, error) {
		return struct{}{}, !slices.ContainsFunc(read.next(), func(info RunInfo) bool { return info.Item == item && info.Question != "" }), nil
	}})
}

// endsWithNoQuestion waits for the end of a run of the item that must not ask the owner. It fails by name when the status of the run shows a question first.
func endsWithNoQuestion(t *testing.T, h *harness, item string, done <-chan runOutcome, what string) {
	t.Helper()

	client := h.client(t, testVersion)
	read := newStatusRead(client)
	asked := ""

	outcome := waitOn(t, waiting[runOutcome]{what: "the end of the run of item " + item, run: runOfClient(t, client), ch: done, last: read.last, read: func() (runOutcome, bool, error) {
		for _, info := range read.next() {
			if info.Item == item && info.Question != "" {
				asked = info.Question

				return runOutcome{}, true, nil
			}
		}

		return runOutcome{}, false, nil
	}})

	if asked != "" {
		t.Fatalf("the run of item %s asked %q: %s", item, asked, what)
	}

	require.NoError(t, outcome.err)
}

func TestHoldScope_AMemberOfAnotherItemWithNoEarlierRunHasNothingToCompareEvenWhenTheRunFixedItsOwnScope(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closingOf = map[string][][]int{"1": {{12}, {12}}, "3": {{20}}}

	done := rig.runFlowAs(t, rig.engine(t), "1", rig.libFlow(fixFirstReview, "1 3", 1))

	endsWithNoQuestion(t, rig.h, "1", done, "the member of item 3 has no first review, so it was compared with the scope of item 1 and the run asked the owner")
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
	assert.Equal(t, 2, rig.h.forge.closingReads, "one read fixes the scope of item 1, one checks item 1, and item 3 has nothing to compare so it is not read")
}

func TestHoldScope_TheMemberOfTheRunItemTakesTheScopeThatTheRunFixedWhenTheCallGivesNoOwn(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	done := rig.runFlowAs(t, rig.engine(t), "1", rig.libFlow(`fix_scope(name = "scope")`, "1", 1))

	question := questionOf(t, rig.h, "1")

	assert.Contains(t, question, "item 1 closes #13 now", "#12 is in the scope that the run fixed, so only #13 is new")
	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, done).err)
}

func TestHoldScope_AnAnswerKeepsOnlyTheIssuesThatTheQuestionAskedAbout(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	done := rig.runFlowAs(t, engine, "9", withRun(rig.libFlow("pass", "1", 2), "9"))

	questionOf(t, rig.h, "9")
	require.NoError(t, rig.h.client(t, testVersion).Reply("9", Reply{Text: "keep #13, #99"}))
	require.NoError(t, receive(t, done).err)

	require.Len(t, rig.merger.given, 2)
	assert.Equal(t, []model.IssueRef{"#13"}, rig.merger.given[1].Kept, "the owner named #99, which the question did not ask about, so it is not kept")
}

func TestHoldScope_ANewRunOfTheSameItemWhoseIssuesDidNotGrowAsksNoQuestion(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}
	engine := rig.engine(t)

	for range 2 {
		endsWithNoQuestion(t, rig.h, "1", rig.runItemOne(t, engine), "a member whose issues did not grow was asked about")
	}

	assert.Equal(t, []string{"1", "1"}, rig.merger.landed())
	assert.Equal(t, 3, rig.h.forge.closingReads, "run one reads twice, and run two keeps the scope of run one and reads once to check")
}

func TestHoldScope_AMemberOfAnotherItemWithAnEarlierRunWhoseIssuesDidNotGrowAsksNoQuestion(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")

	endsWithNoQuestion(t, rig.h, "9", rig.runMergeAs(t, engine, "9", "1"), "a member whose issues did not grow was asked about")

	assert.Equal(t, []string{"1"}, rig.merger.landed())
	assert.Equal(t, 2, rig.h.forge.closingReads, "the earlier run read once, and the check reads once")
}

func TestFixScope_TheItemArgumentChoosesTheItemOfTheReadAndOfTheEarlierReads(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closingOf = map[string][][]int{"1": {{12}}, "3": {{20}, {20, 21}}}
	engine := rig.engine(t)
	text := scopeLoad + "def main(item):\n    s = fix_scope(name = \"scope\", item = \"3\")\n    command(name = \"report\", run = [\"report\", \",\".join(s.issues)])\n"

	_, err := engine.RunFile("1", testProject, writeFlow(t, text), modeOff())
	require.NoError(t, err)
	assert.Equal(t, []string{"report #20"}, rig.h.commands.ran(), "the read is the pull request of item 3, not the one of the run item")

	reads := rig.h.forge.closingReads
	_, err = engine.RunFile("3", testProject, writeFlow(t, scopeLoad+"def main(item):\n    "+fixFirstReview+"\n"), modeOff())
	require.NoError(t, err)

	_, err = engine.RunFile("1", testProject, writeFlow(t, text), modeOff())
	require.NoError(t, err)
	assert.Equal(t, reads+1, rig.h.forge.closingReads, "the run of item 3 read once, and the second call for item 3 found its scope in the earlier run of item 3 and read nothing")
	assert.Equal(t, "report #20,#21", rig.h.commands.ran()[len(rig.h.commands.ran())-1], "the scope of item 3 is the one that its own run fixed")
}

func TestHoldScope_ANewIssueOfAMemberWhoseFirstReviewClosedNoIssueIsAskedAboutWithTheWordsNoIssue(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{}, {13}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	done := rig.runMergeAs(t, engine, "9", "1")

	question := questionOf(t, rig.h, "9")

	assert.Contains(t, question, "did not at its first review (then: no issue)")
	require.NoError(t, rig.h.client(t, testVersion).Reply("9", Reply{Text: "no"}))
	require.NoError(t, receive(t, done).err)
}

// mergeForm is one form of a merge call over the members that hold_scope let through: the argument that passes them, and the expression of the refusal reasons in its result.
type mergeForm struct{ members, reasons string }

// mergeForms are the forms of a merge call: the members of one union.
func mergeForms() map[string]mergeForm {
	return map[string]mergeForm{
		"members": {members: "members = held.prs", reasons: `",".join([r.reason for r in out.rejected])`},
	}
}

// heldMergeFlow fixes the scope, checks the pull request of item 1, merges it with the form and the owner identities, and reports the refusal reasons of the result.
func (r *batchRig) heldMergeFlow(form mergeForm) string {
	return scopeLoad + "def main(item):\n    " + fixFirstReview + "\n" +
		`    held = hold_scope(name = "h", own = own, prs = [struct(item = "1", head = "` + r.heads["1"] + `", closes = [])])` + "\n" +
		`    out = merge(` + form.members + `, base = "main", repo = "o/r", own = {"logins": ["` + shipOwner + `"], "emails": ["` + shipMail + `"]}, name = "b")` + "\n" +
		`    command(name = "report", run = ["report", ` + form.reasons + `])` + "\n"
}

// keepThirteen runs the workflow text as item 1 over a scope of #12 and a check that reads #12 and #13, answers "keep #13", and returns the report of the run.
func keepThirteen(t *testing.T, rig *batchRig, engine *Engine, text string) string {
	t.Helper()

	done := rig.runFlowAs(t, engine, "1", text)

	questionOf(t, rig.h, "1")
	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, done).err)

	ran := rig.h.commands.ran()
	require.Len(t, ran, 1)

	return ran[0]
}

func TestHoldScope_EachFormOfMergeGivesTheMergerTheKeptIssuesAndTheCheckedSetOfTheMember(t *testing.T) {
	t.Parallel()

	for name, form := range mergeForms() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)
			rig.h.forge.closing = [][]int{{12}, {12, 13}}
			keepThirteen(t, rig, rig.engine(t), rig.heldMergeFlow(form))

			require.Len(t, rig.merger.given, 1)
			assert.Equal(t, []model.IssueRef{"#13"}, rig.merger.given[0].Kept, "the closing keyword guard of the merge may name the kept issue")
			assert.Equal(t, []model.IssueRef{"#12", "#13"}, rig.merger.given[0].Closing, "the merge pins the set that the check read")
		})
	}
}

func TestHoldScope_EachFormOfMergeRefusesAMemberWhoseClosingSetChangedAfterTheCheck(t *testing.T) {
	t.Parallel()

	for name, form := range mergeForms() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)
			rig.h.forge.closing = [][]int{{12}, {12, 13}}
			world := &shipWorld{pull: shipPull(t, rig.heads["1"], map[string]any{"baseRefName": "main"})}
			deps := rig.deps()
			deps.Merge = &fakeMerge{over: mergerOver(world)}

			engine, err := NewEngine(t.Context(), rig.h.opts, deps)
			require.NoError(t, err)
			require.NoError(t, engine.Launch())
			t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

			report := keepThirteen(t, rig, engine, rig.heldMergeFlow(form))

			assert.Contains(t, report, "closes no issue now, and closed #12 #13 when the scope check read it", "the forge lists no issue at the merge, so the pinned set refuses")
			assert.Empty(t, world.writes, "a refused member has had no write on the forge")
		})
	}
}

// keptRefusal is the start of the error of a merge whose member keeps #13 with no owner answer that keeps it.
const keptRefusal = "item 1 keeps #13, and no owner answer keeps it"

// keptMember is the member of item 1 that keeps #13, in the form of the merge call.
func (r *batchRig) keptMember(form string) string {
	member := `struct(item = "1", head = "` + r.heads["1"] + `", closes = [], kept = ["#13"])`
	return form + " = [" + member + "]"
}

// keptFlow runs the statement pre, then merges the member of item 1 that keeps #13 in the form, and reports the merged items.
func (r *batchRig) keptFlow(form, pre string) string {
	return "def main(item):\n    " + pre + "\n    merge(" + r.keptMember(form) + `, base = "main", repo = "o/r", name = "b")` + "\n"
}

// askToKeep asks the owner under the name keep-1, as hold_scope does for the member of item 1.
const askToKeep = `wait(name = "keep-1", question = "keep #13?")`

// answerKeep runs the text as the item, answers its one question with the line, and returns the outcome of the run.
func (r *batchRig) answerKeep(t *testing.T, engine *Engine, item, text, line string) runOutcome {
	t.Helper()

	done := r.runFlowAs(t, engine, item, text)

	questionOf(t, r.h, item)
	require.NoError(t, r.h.client(t, testVersion).Reply(item, Reply{Text: line}))

	return receive(t, done)
}

func TestMerge_AKeptIssueThatNoOwnerAnswerKeptEndsTheCallAndMergesNothingInEachForm(t *testing.T) {
	t.Parallel()

	for name := range mergeForms() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)
			_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, rig.keptFlow(name, "pass")), modeOff())

			require.ErrorContains(t, err, keptRefusal)
			assert.Empty(t, rig.merger.given, "a made-up kept issue reaches no merge")
		})
	}
}

func TestMerge_AKeptIssueThatAnOwnerAnswerOfThisRunKeptReachesTheMergeInEachForm(t *testing.T) {
	t.Parallel()

	for name := range mergeForms() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)
			out := rig.answerKeep(t, rig.engine(t), "1", rig.keptFlow(name, askToKeep), "keep #13")

			require.NoError(t, out.err)
			require.Len(t, rig.merger.given, 1)
			assert.Equal(t, []model.IssueRef{"#13"}, rig.merger.given[0].Kept)
		})
	}
}

func TestMerge_AnOwnerAnswerThatKeepsAnotherIssueDoesNotKeepTheIssueOfTheMember(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	out := rig.answerKeep(t, rig.engine(t), "1", rig.keptFlow("members", askToKeep), "keep #14")

	require.ErrorContains(t, out.err, keptRefusal)
	assert.Empty(t, rig.merger.given)
}

func TestMerge_AKeptIssueThatAnOwnerAnswerOfAnEarlierRunOfTheItemKeptReachesTheMerge(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	engine := rig.engine(t)
	require.NoError(t, rig.answerKeep(t, engine, "1", "def main(item):\n    "+askToKeep+"\n", "keep #13").err)

	_, err := engine.RunFile("1", testProject, writeFlow(t, rig.keptFlow("members", "pass")), modeOff())

	require.NoError(t, err)
	require.Len(t, rig.merger.given, 1)
	assert.Equal(t, []model.IssueRef{"#13"}, rig.merger.given[0].Kept)
}

func TestMerge_AKeptIssueThatOnlyAnAnswerInARunOfAnotherItemKeptEndsTheCall(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	engine := rig.engine(t)
	require.NoError(t, rig.answerKeep(t, engine, "9", "def main(item):\n    "+askToKeep+"\n", "keep #13").err)

	_, err := engine.RunFile("1", testProject, writeFlow(t, rig.keptFlow("members", "pass")), modeOff())

	require.ErrorContains(t, err, keptRefusal)
	assert.Empty(t, rig.merger.given, "the answer of a run of item 9 keeps nothing for a run of item 1")
}

func TestMerge_AKeptIssueWhoseOwnerAnswersCannotBeReadEndsTheCall(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	s := &script{e: engine, in: ItemInput{Item: testItem}}

	require.ErrorContains(t, s.trustMemberKept(workflow.PullRequest{Item: "1", Kept: []model.IssueRef{"#13"}}), "list the earlier runs of item 12")
	require.NoError(t, s.trustMemberKept(workflow.PullRequest{Item: "1"}), "a member that keeps nothing reads no answer")
}
