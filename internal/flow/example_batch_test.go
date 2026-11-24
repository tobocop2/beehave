package flow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
)

// remoteBranches is a preflight reader over the branches on the remote: the head of each branch, and no entry for a branch that is gone.
// It gives the stops that the real check gives for a branch that is gone and for a head that is not the head the call expects.
type remoteBranches struct {
	noReads

	mu    sync.Mutex
	heads map[string]string
	asked []string
	// drafts is the number of the pull request of each item whose pull request is a draft. A read for such an item gives that draft, and claim_stop stops at it unless the call names that number as its own draft.
	drafts map[string]int
	// assigned is the logins that hold the pull request of each item. A read for such an item gives an open pull request that they hold, and the merge itself checks no assignee.
	assigned map[string][]string

	// before runs at the start of each call, with no lock held. The round rig holds the turn of a member here: the preflight holds no queue slot.
	before func(ctx context.Context, item string)
}

// ReadPull gives the draft of the item, written by the owner of the round, or no pull request.
func (r *remoteBranches) ReadPull(_ context.Context, _, item string) (model.PullFacts, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if holders := r.assigned[item]; len(holders) > 0 {
		return model.PullFacts{Number: testPull, State: "OPEN", Author: shipOwner, Assignees: holders}, nil
	}

	if r.drafts[item] == 0 {
		return model.PullFacts{}, nil
	}

	return model.PullFacts{Number: r.drafts[item], State: "OPEN", Draft: true, Author: shipOwner}, nil
}

func (r *remoteBranches) Preflight(ctx context.Context, _, item string, req preflight.Request) (preflight.Result, error) {
	if r.before != nil {
		r.before(ctx, item)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.asked = append(r.asked, item)

	head, present := r.heads[item]

	switch {
	case !present:
		return preflight.Result{Stop: preflight.BranchGone, Lines: []string{"STOP: the head branch of item " + item + " no longer exists"}}, nil
	case req.ExpectHead != "" && req.ExpectHead != head:
		return preflight.Result{Stop: preflight.HeadMoved, Lines: []string{"STOP: head moved to " + head}}, nil
	}

	return preflight.Result{Stop: preflight.Unchanged}, nil
}

// heldOf returns the items that the run put on hold.
func heldOf(w *fakeWriter) []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	var held []string

	for _, status := range w.statuses {
		if strings.HasSuffix(status, "|hold") {
			held = append(held, status)
		}
	}

	return held
}

// notesWithKey returns the comments of the run that carry the key.
func notesWithKey(w *fakeWriter, key string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	var notes []string

	for _, note := range w.notes {
		// The key of a comment is scoped to the run, so the text tells a merge record from a rejection.
		if strings.Contains(note, "Merged into") == (key == "merged") {
			notes = append(notes, note)
		}
	}

	return notes
}

// gitLines returns the git commands that the run made, in order.
func gitLines(rig *roundRig) []string {
	var lines []string

	for _, line := range rig.h.commands.ran() {
		if strings.HasPrefix(line, "git fetch") || strings.HasPrefix(line, "git diff") {
			lines = append(lines, line)
		}
	}

	return lines
}

func TestExampleBatch_AnApprovedItemLandsWhileItsPullRequestIsStillTheDraftItsRunOpened(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.h.forge.onCreate = func(item string) {
		rig.remote.mu.Lock()
		defer rig.remote.mu.Unlock()

		rig.remote.drafts = map[string]int{item: rig.h.forge.state.Number}
	}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the batch intake accepts the draft that the run opened")
	assert.Empty(t, heldOf(rig.writer), "the item does not stop at its own draft one stage after the loop")
}

func TestExampleBatch_ItemsThatAreDoneAtOneQueueReadLandAsOneBatchAndTheSplitHoldsTheRedOne(t *testing.T) {
	t.Parallel()

	// Item 2 ends alone in pass 1 and is red at the gate. Items 1 and 3 end while the round reads the queue of pass 2, which shows all three as done: one batch.
	rig := newRoundRig(t, "1 2 3")
	rig.h.opts.MaxConcurrent = 3 // all three children start in pass 1
	rig.endTogether("1", "3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
	assert.Len(t, rig.gate.ran(), 4, "one gate run on the union of the three items, then the gate runs of the split of the red union")
	assert.Equal(t, []string{testProject + "|2|hold"}, heldOf(rig.writer))
	assert.Equal(t, []string{testProject + " main 0 1,3"}, rig.host.issueReads(), "one read after the one batch that landed both items; the batch that landed nothing makes no read")
	assert.Equal(t, "README\none.txt\nthree.txt", rig.mainFiles(t))

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.Contains(t, joined, "round-land-2.union#", "the pass 2 lands items 1 and 3 in one batch")
	assert.NotContains(t, joined, "round-land-3.union#", "no third batch")
	landedAt, bodyAt := strings.Index(joined, "round-land-2.union#"), strings.Index(joined, "integration_pull:round-integration-land-2#")
	require.GreaterOrEqual(t, bodyAt, 0, "the round makes the body of the integration pull request again after a landing")
	assert.Greater(t, bodyAt, landedAt, "and it does so after the batch that landed")

	for _, given := range rig.merger.given {
		assert.Equal(t, "hold", given.Hold, "the guards of the merge of one item apply")
		assert.Equal(t, []string{shipOwner}, given.Logins)
		assert.Equal(t, []string{shipMail}, given.Emails)
		assert.Len(t, given.ForbidText, 4)
		assert.True(t, given.Approved, "each member carries the approval of the review of its child run")
	}
}

func TestExampleBatch_AnItemThatAGuardRefusesIsHeldAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Lines: []string{"REFUSE: has the label hold"}, Refusal: []string{"REFUSE: has the label hold"}}}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, "README\nthree.txt", rig.mainFiles(t), "item 3 lands and item 1 does not")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "REFUSE: has the label hold")
}

func TestExampleBatch_APushAfterTheReviewRefusesOnlyThatItem(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.pushAfterReviews("1", strings.Repeat("e", 40)) // somebody pushed to the branch of item 1 after the review approved it

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the pushed item never reaches the merge")
	assert.Equal(t, "README\nthree.txt", rig.mainFiles(t))
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "head moved")
}

func TestExampleBatch_EachMemberMergesAtThePinOfItsChild(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{rig.heads["1"], rig.heads["3"]}, rig.merger.pinned(), "each member merges at its pin")
}

// When the intake stops every approved item, no merge call runs. The stops hold the items, which changes their rows, so the next pass reads the queue before the round waits.
func TestExampleBatch_APassThatStopsEveryItemAtTheIntakeReadsTheQueueAgainBeforeTheRoundWaits(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.pushAfterReviews("1", strings.Repeat("e", 40))

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, rig.merger.landed(), "no item reaches the merge")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))

	reads := 0
	for _, name := range stepNames(t, rig.last) {
		if strings.HasPrefix(name, "queue:round-queue-") {
			reads++
		}
	}

	assert.Equal(t, 3, reads, "the pass that held the item is followed by a pass that reads the queue, and then the round waits")
}

func TestExampleBatch_AMissingBranchIsReportedAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.pushAfterReviews("1", "") // the branch of item 1 is deleted after the review approved it

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed())
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "no longer exists")
}

func TestExampleBatch_AnItemWithNoChangelogEntryIsHeldWhenTheProjectDeclaresOneAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.project.Vars["changelog"] = "CHANGELOG.md"

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[1] == "diff" && rig.landing("1") && strings.HasSuffix(request.Run[len(request.Run)-1], rig.heads["1"]) {
			return CommandOutput{Stdout: "one.txt\n"}, nil
		}

		if request.Run[1] == "diff" {
			return CommandOutput{Stdout: "three.txt\nCHANGELOG.md\n"}, nil
		}

		return previous(request)
	}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the item with no entry never reaches the merge")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "the project declares the changelog CHANGELOG.md")

	for _, item := range []string{"1", "3"} {
		head := rig.heads[item]
		own := slices.DeleteFunc(gitLines(rig), func(line string) bool { return !strings.HasSuffix(line, head) })
		require.GreaterOrEqual(t, len(own), 2, "item %s", item)
		assert.Equal(t, []string{"git fetch origin " + head, "git diff --name-only origin/main..." + head}, own[len(own)-2:], "after the check of its child, the member is fetched, then read from the base to its own head")
	}
}

func TestExampleBatch_TheChangelogChecksOfTwoItemsRecordTheirOwnSteps(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.project.Vars["changelog"] = "CHANGELOG.md"

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "git" && (request.Run[1] == "fetch" || request.Run[1] == "diff") {
			return CommandOutput{Stdout: "CHANGELOG.md\n"}, nil
		}

		return previous(request)
	}

	_, err := rig.run(t)
	require.NoError(t, err)

	joined := strings.Join(stepNames(t, rig.last), " ")
	for _, id := range []string{"1", "3"} {
		assert.Contains(t, joined, "changelog-fetch-"+id+"#")
		assert.Contains(t, joined, "changed-paths-"+id+"#")
	}
}

func TestExampleBatch_AnItemWhoseHeadCannotBeFetchedIsHeldWithTheReasonAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.project.Vars["changelog"] = "CHANGELOG.md"

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[1] == "fetch" && request.Run[len(request.Run)-1] == rig.heads["1"] {
			return CommandOutput{RC: 1, Stderr: "fatal: no such ref\n"}, nil
		}

		if request.Run[0] == "git" && (request.Run[1] == "fetch" || request.Run[1] == "diff") {
			return CommandOutput{Stdout: "CHANGELOG.md\n"}, nil
		}

		return previous(request)
	}

	_, err := rig.run(t)

	require.NoError(t, err, "a code of 1 is a result, not a stop of the run")
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the item that cannot be checked never reaches the merge")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "could not be fetched")
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "fatal: no such ref")
	diff := "git diff --name-only origin/main..." + rig.heads["1"]
	diffs := slices.DeleteFunc(gitLines(rig), func(line string) bool { return line != diff })
	assert.Len(t, diffs, 1, "the one diff of item 1 is the check of its child, before the batch: no diff runs after the failed fetch")
}

func TestExampleBatch_AnItemWhoseChangedPathsCannotBeReadIsHeldWithTheReasonAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.project.Vars["changelog"] = "CHANGELOG.md"

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if rig.landing("1") && request.Run[1] == "diff" && strings.HasSuffix(request.Run[len(request.Run)-1], rig.heads["1"]) {
			return CommandOutput{RC: 1, Stderr: "fatal: bad revision\n"}, nil
		}

		if request.Run[0] == "git" && (request.Run[1] == "fetch" || request.Run[1] == "diff") {
			return CommandOutput{Stdout: "CHANGELOG.md\n"}, nil
		}

		return previous(request)
	}

	_, err := rig.run(t)

	require.NoError(t, err, "a code of 1 is a result, not a stop of the run")
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the item that cannot be checked never reaches the merge")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "changed paths of item 1 could not be read")
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "fatal: bad revision")
}

func TestExampleBatch_EveryItemGetsTheStopsOfOneItemBeforeTheBatch(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Len(t, rig.remote.asked, 2*readsToLanding, "each member is read four times: at its intake, before its scoped review, before its full review and once more before its own batch")

	for _, item := range []string{"1", "3"} {
		assert.Equal(t, readsToLanding, strings.Count(strings.Join(rig.remote.asked, " "), item), "item %s", item)
	}
}

func TestExampleBatch_AMergeSettingThatIsOffLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.setRound("merge", false)

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, rig.merger.asked, "no member reaches the merge")
	assert.Empty(t, rig.gate.ran(), "no gate runs")
	assert.Len(t, rig.remote.asked, 2*readsBeforeReview, "only the intake and the review loop of the two children read a branch; the batch reads none")
	assert.Empty(t, heldOf(rig.writer))
	assert.Equal(t, "README", rig.mainFiles(t))
}

func TestExampleBatch_AHandOffRoundMarksTheApprovedPullRequestsReadyAndHoldsTheItemWhoseHeadMoved(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.setRound("hand_off", true)
	rig.host.pr = &handForge{views: map[string][]fakePull{
		"1": {{State: "OPEN", Draft: true, Head: rig.heads["1"]}, {State: "OPEN", Head: rig.heads["1"]}, {State: "MERGED", Head: rig.heads["1"]}},
		"3": {{State: "OPEN", Draft: true, Head: rig.heads["3"]}, {State: "OPEN", Head: "beef"}},
	}, reads: map[string]int{}, draftErr: map[bool]error{}}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, rig.merger.asked, "the owner merges: the round lands nothing itself")
	assert.Empty(t, rig.gate.ran(), "a hand-off runs no gate of the union")
	assert.ElementsMatch(t, []string{"1:false", "3:false", "3:true"}, rig.host.pr.marked(), "both are marked ready, and the one whose head moved is a draft again")
	assert.Equal(t, []string{testProject + "|1|closed"}, closedOf(rig.writer), "the item that the owner merged is recorded as merged")
	assert.Equal(t, []string{testProject + "|3|hold"}, heldOf(rig.writer), "the item whose head moved holds")
	assert.Contains(t, strings.Join(rig.writer.notes, "\n"), "the head moved to beef after the head "+rig.heads["3"]+" was marked ready", "the item gets the reason as a comment")
}

func TestExampleBatch_OneApprovedItemLandsAsABatchOfOne(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed())
}

func TestExampleBatch_TheIntegrationPullRequestOfTheRoundReachesTheReadAfterTheBatch(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.setRound("integration_pr", json.Number("7"))

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + " main 7 1", testProject + " main 7 3"}, rig.host.issueReads(), "the members end in turn, so each lands in its own batch and each read has the number")
}

func TestExampleBatch_ARoundWithNoIntegrationPullRequestReadsWithNumberZero(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + " main 0 1", testProject + " main 0 3"}, rig.host.issueReads())
}

func TestExampleBatch_AFindingStopsTheRunWithARefuseForTheOwner(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.host.report = batch.Report{Findings: []string{"issue #1 of item 1 is not closed after the batch landed"}}

	_, err := rig.run(t)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "issue #1 of item 1 is not closed after the batch landed")
	assert.Contains(t, err.Error(), "by hand")
}

func TestExampleBatch_AnUnprovenMemberStopsTheRunWithARefuseThatNamesTheItemAndTheReason(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.host.unproven = []batch.Outcome{{Item: "3", Reason: "its branch holds another commit now"}}

	_, err := rig.run(t)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "item 3: its branch holds another commit now")
}

func TestExampleBatch_ANoteIsPrintedAsAnIssueCheckThatDidNotRun(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.host.report = batch.Report{Note: "the base is not the default branch"}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, notesWithKey(rig.writer, "rejected"), "the round has no item issue for the note, so it prints it in the run record")
}

func TestExampleBatch_AReportWithNoFindingAndNoNoteSaysNothing(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, notesWithKey(rig.writer, "rejected"))
}

func TestExampleBatch_EachMergedItemIsRecordedAsMergedAndClosed(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|1|closed", testProject + "|3|closed"}, closedOf(rig.writer))
	assert.Len(t, notesWithKey(rig.writer, "merged"), 2)
}

// closedOf returns the items that the run closed.
func closedOf(w *fakeWriter) []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	var closed []string

	for _, status := range w.statuses {
		if strings.HasSuffix(status, "|closed") {
			closed = append(closed, status)
		}
	}

	return closed
}

func TestExampleSwarm_TheLandCallPassesTheIntegrationAndActsOnFindingsAndNote(t *testing.T) {
	t.Parallel()

	text := string(mustRead(t, filepath.Join("..", "..", "examples", "swarm", "workflow.star")))

	assert.Contains(t, text, `base = env("BASE")`)
	assert.Contains(t, text, `repo = env("REPO")`)
	assert.Contains(t, text, "out.merged")
	assert.Contains(t, text, "out.rejected")
	assert.Contains(t, text, "out.deferred")
	assert.Contains(t, text, `"Not landed: " + rejected.reason`)
	assert.Contains(t, text, "the forge could not settle")
}
