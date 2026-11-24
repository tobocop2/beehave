package flow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

// fakePull is the pull request of a test of a hand-off, as the host serves it. State is OPEN, CLOSED or MERGED.
type fakePull struct {
	State          string
	Draft          bool
	Head           string
	Conflicting    bool
	ChangeRequests int
	Threads        int
}

// handForge is the forge side of a hand-off in a test. Each item has a script of views: a read takes the next one and the last one repeats.
type handForge struct {
	mu       sync.Mutex
	views    map[string][]fakePull
	reads    map[string]int
	drafts   []string
	pullErr  error
	draftErr map[bool]error
	// same holds the heads that have the tree of the pinned head. treeErr is the error of the tree read.
	same    map[string]bool
	treeErr error
	// onRead, when set, runs at each read with the number of the read of that item, counted from 1.
	onRead func(item string, read int)
	// blocked holds the items whose read waits for the end of its limit, when it has one.
	blocked map[string]bool
	// overran holds the item whose pull request the watch read more than handReadCap times.
	overran string
}

// handReadCap is the most reads of one pull request that a test of the hand-off allows before the test fails.
const handReadCap = 200

func (f *handForge) pull(item string) (fakePull, error) {
	f.mu.Lock()
	f.reads[item]++
	read, script := f.reads[item], f.views[item]
	hook := f.onRead
	f.mu.Unlock()

	if hook != nil {
		hook(item, read)
	}

	// A watch that no owner ends reads for ever. The read past the cap closes the pull request so that the call ends, and the test fails with the item.
	if read > handReadCap {
		f.mu.Lock()
		f.overran = item
		f.mu.Unlock()

		return fakePull{State: "CLOSED"}, nil
	}

	if f.pullErr != nil {
		return fakePull{}, f.pullErr
	}

	return script[min(read, len(script))-1], nil
}

func (f *handForge) setDraft(item string, draft bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.draftErr[draft]; err != nil {
		return err
	}

	f.drafts = append(f.drafts, fmt.Sprintf("%s:%t", item, draft))

	return nil
}

func (f *handForge) marked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.drafts...)
}

// Pull serves the standing of the pull request fake. As the adapter does with the standing review, a head that is not reviewed and is named in same is a rewrite of it,
// and a tree that cannot be read goes into Unread.
func (h *repoHost) Pull(ctx context.Context, _, item, _, reviewed string) (review.Standing, error) {
	if err := ctx.Err(); err != nil {
		return review.Standing{}, err
	}

	// A read of a blocked item that has a limit, as the reads of a draft after a cancel have, takes all the time of the limit.
	if _, limited := ctx.Deadline(); limited && h.pr.blocked[item] {
		<-ctx.Done()

		return review.Standing{}, ctx.Err()
	}

	view, err := h.pr.pull(item)
	if err != nil {
		return review.Standing{}, err
	}

	standing := review.Standing{Head: view.Head, Merged: view.State == "MERGED", Closed: view.State == "CLOSED", Draft: view.Draft, Conflicting: view.Conflicting, UnresolvedThreads: view.Threads}
	standing.ChangeRequests = make([]model.ChangeRequest, view.ChangeRequests)

	if reviewed != "" && view.Head != reviewed && view.Head != "" {
		if h.pr.treeErr != nil {
			standing.Unread = append(standing.Unread, "tree of the head")
		} else if h.pr.same[view.Head] {
			standing.Rewritten = reviewed
		}
	}

	return standing, nil
}

func (h *repoHost) SetDraft(_ context.Context, _, item, _ string, draft bool) error {
	return h.pr.setDraft(item, draft)
}

// handRig is a batch rig whose host serves the pull requests of a hand-off. Each view is written as state, draft flag and head.
type handRig struct {
	*batchRig
}

func newHandRig(t *testing.T) *handRig {
	t.Helper()

	rig := &handRig{batchRig: newBatchRig(t)}
	rig.host.pr = &handForge{views: map[string][]fakePull{}, reads: map[string]int{}, draftErr: map[bool]error{}}
	rig.h.facts = notifyFacts(notifyProgram)

	t.Cleanup(func() {
		rig.host.pr.mu.Lock()
		defer rig.host.pr.mu.Unlock()

		assert.Empty(t, rig.host.pr.overran, "the watch read the pull request of this item more than %d times: it waits for an owner who does not merge", handReadCap)
	})

	return rig
}

func (r *handRig) script(item string, views ...fakePull) {
	r.host.pr.views[item] = views
}

// view makes a view of the pull request of an item at its own head, or at the head given.
func (r *handRig) view(item, state string, draft bool, head ...string) fakePull {
	at := r.heads[item]
	if len(head) > 0 {
		at = head[0]
	}

	return fakePull{State: state, Draft: draft, Head: at}
}

// body writes a hand-off call over the listed items and a command that reports the result.
func (r *handRig) body(items, extra string) string {
	var prs []string

	for _, item := range strings.Fields(items) {
		prs = append(prs, `struct(item = "`+item+`", head = "`+r.heads[item]+`", closes = [])`)
	}

	return `out = merge(prs = [` + strings.Join(prs, ", ") + `], base = "main", repo = "o/r", hand_off = True, ` + extra + `name = "b")
command(name = "report", run = ["report", ",".join(out.merged), ",".join([m.item + "@" + m.head for m in out.moved]), ",".join([r.item + ":" + r.reason for r in out.rejected]), str(out.gates)])
`
}

func TestHandOff_AMergedPullRequestEndsTheWaitAndTheOwnerIsToldOnce(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.view("1", "OPEN", false), rig.view("1", "MERGED", false))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "the draft is marked ready once and nothing is drafted again")
	assert.Equal(t, 4, rig.host.pr.reads["1"], "one read to mark it ready, then one read for each round of the watch")
	assert.Len(t, notifyRequests(rig.h), 1, "the owner is told once for the whole wait")
	assert.Equal(t, []string{"report 1   0"}, filterReport(rig.h.commands.ran()))
	assert.Empty(t, rig.merger.landed(), "a hand-off lands nothing")
	assert.Empty(t, rig.gate.ran(), "a hand-off runs no gate")
}

func TestHandOff_AHeadThatMovesAfterTheReadyMarkPutsThePullRequestBackToDraftAndReturnsTheNewHead(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false, "moved1"))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false", "1:true"}, rig.host.pr.marked(), "marked ready, then back to draft")
	assert.Equal(t, []string{"report  1@moved1 1:the head moved to moved1 after the head " + rig.heads["1"] + " was marked ready 0"}, filterReport(rig.h.commands.ran()))
	assert.Len(t, notifyRequests(rig.h), 1)
}

func TestHandOff_AHeadThatMovedBeforeTheReadyMarkMarksNothingReady(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true, "moved1"))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Empty(t, rig.host.pr.marked(), "a draft at another head is not marked ready and is already a draft")
	assert.Empty(t, notifyRequests(rig.h), "no member waits, so no owner is told")
	assert.Equal(t, []string{"report  1@moved1 1:the head moved to moved1 after the head " + rig.heads["1"] + " was marked ready 0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AClosedPullRequestLeavesTheWatchWithItsReason(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "CLOSED", false))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "a closed pull request is not drafted")
	assert.Equal(t, []string{"report   1:the pull request is closed 0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_TwoMembersWaitTogetherAndTheOneThatMovedDoesNotEndTheOther(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.view("1", "MERGED", false))
	rig.script("2", rig.view("2", "OPEN", false), rig.view("2", "OPEN", false, "moved2"))

	_, err := rig.walk(t, rig.body("1 2", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false", "2:true"}, rig.host.pr.marked(), "a ready pull request is not marked again, and the moved one is drafted")
	assert.Len(t, notifyRequests(rig.h), 1, "one notice for the whole call")
	assert.Equal(t, []string{"report 1 2@moved2 2:the head moved to moved2 after the head " + rig.heads["2"] + " was marked ready 0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AResumeDuringTheWaitMarksNothingReadyTwiceAndTellsTheOwnerNoMoreTimes(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.view("1", "MERGED", false))
	rig.host.pr.onRead = func(_ string, read int) {
		if read != 2 {
			return
		}

		if err := rig.h.client(t, testVersion).Cancel(testItem); err != nil {
			t.Error(err)
		}

		rig.h.tick(t)
	}
	file := writeFlow(t, bodyFlow(rig.body("1", "")))
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file, modeOff())

	require.Error(t, err)
	_, err = resumeAndJoin(t, engine, rig.h.client(t, testVersion))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "the resume replays the ready step and marks nothing ready again")
	assert.Len(t, notifyRequests(rig.h), 1, "the resume tells the owner no more times")
	assert.Equal(t, []string{"report 1   0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AReadThatFailsEndsTheCallAndNamesTheItem(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.host.pr.pullErr = errors.New("forge is down")

	_, err := rig.walk(t, rig.body("1", ""))

	require.ErrorContains(t, err, "read the pull request of item 1: forge is down")
}

func TestHandOff_AReadyMarkThatFailsEndsTheCallAndNamesTheItem(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true))
	rig.host.pr.draftErr[false] = errors.New("no permission")

	_, err := rig.walk(t, rig.body("1", ""))

	require.ErrorContains(t, err, "mark the pull request of item 1 ready: no permission")
}

func TestHandOff_ADraftCallThatFailsAfterAMovedHeadTellsTheOwnerToConvertByHand(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", false, "moved1"))
	rig.host.pr.draftErr[true] = errors.New("no permission")

	_, err := rig.walk(t, rig.body("1", ""))

	require.ErrorContains(t, err, "the head of item 1 moved to moved1. Cannot make its pull request a draft again. If it is open and ready for review, convert it to a draft by hand: no permission")
}

func TestHandOff_AKeywordOfALandingIsRefused(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"hold":        `hold = "hold", `,
		"own":         `own = {"logins": ["me"]}, `,
		"forbid_text": `forbid_text = ["x"], `,
		"gate_run":    `gate_run = ["check"], `,
		"integration": `integration = 5, `,
	}

	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newHandRig(t)
			rig.script("1", rig.view("1", "OPEN", true))

			// The flag is a value that only the run knows, so the static check passes the call and the refusal of the run is the one that answers.
			body := "flag = True\n" + strings.Replace(rig.body("1", extra), "hand_off = True", "hand_off = flag", 1)
			_, err := rig.walk(t, body)

			require.ErrorContains(t, err, "merge with `hand_off` lands nothing, so it takes no `"+name+"`")
			assert.Zero(t, rig.host.pr.reads["1"], "a refused call reads nothing")
		})
	}
}

// filterReport keeps the report commands of the flow and drops the notify commands.
func filterReport(ran []string) []string {
	var out []string

	for _, line := range ran {
		if strings.HasPrefix(line, "report ") {
			out = append(out, line)
		}
	}

	return out
}

func TestHandOff_ACancelAfterTheReadyMarkStopsTheCallAndAResumeGoesOn(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "MERGED", false))
	rig.host.pr.onRead = func(_ string, read int) {
		if read != 1 {
			return
		}

		if err := rig.h.client(t, testVersion).Cancel(testItem); err != nil {
			t.Error(err)
		}

		rig.h.tick(t)
	}
	file := writeFlow(t, bodyFlow(rig.body("1", "")))
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file, modeOff())

	require.Error(t, err)
	_, err = resumeAndJoin(t, engine, rig.h.client(t, testVersion))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked())
}

// standing makes a view of an item with the facts that hold a pull request back.
func (r *handRig) standing(item string, draft bool, set func(*fakePull)) fakePull {
	v := r.view(item, "OPEN", draft)
	set(&v)

	return v
}

func TestHandOff_AStandingReasonHoldsTheReadyMarkAndTheCallNamesIt(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		set    func(*fakePull)
		reason string
	}{
		"a change request": {func(v *fakePull) { v.ChangeRequests = 1 }, "a review asks for changes"},
		"a conflict":       {func(v *fakePull) { v.Conflicting = true }, "it conflicts with its base branch"},
		"a thread":         {func(v *fakePull) { v.Threads = 2 }, "2 review threads are unresolved"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newHandRig(t)
			rig.script("1", rig.standing("1", true, tc.set))

			_, err := rig.walk(t, rig.body("1", ""))

			require.NoError(t, err)
			assert.Empty(t, rig.host.pr.marked(), "a draft with a reason to wait is not marked ready")
			assert.Empty(t, notifyRequests(rig.h), "no member waits, so no owner is told")
			assert.Equal(t, []string{"report   1:not marked ready: " + tc.reason + " 0"}, filterReport(rig.h.commands.ran()))
		})
	}
}

func TestHandOff_AReadyPullRequestWithAChangeRequestOrAConflictGoesBackToDraftAtOnce(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.standing("1", false, func(v *fakePull) { v.ChangeRequests = 1; v.Conflicting = true }))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:true"}, rig.host.pr.marked())
	assert.Equal(t, []string{"report   1:drafted again: a review asks for changes, it conflicts with its base branch 0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AChangeRequestOrAConflictDuringTheWatchPutsThePullRequestBackToDraft(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		set    func(*fakePull)
		reason string
	}{
		"a change request": {func(v *fakePull) { v.ChangeRequests = 1 }, "a review asks for changes"},
		"a conflict":       {func(v *fakePull) { v.Conflicting = true }, "it conflicts with its base branch"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newHandRig(t)
			rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.standing("1", false, tc.set))

			_, err := rig.walk(t, rig.body("1", ""))

			require.NoError(t, err)
			assert.Equal(t, []string{"1:false", "1:true"}, rig.host.pr.marked(), "marked ready, then back to draft")
			assert.Equal(t, []string{"report   1:drafted again: " + tc.reason + " 0"}, filterReport(rig.h.commands.ran()))
		})
	}
}

func TestHandOff_AnUnresolvedThreadDuringTheWatchLeavesThePullRequestReady(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.standing("1", false, func(v *fakePull) { v.Threads = 3 }), rig.view("1", "MERGED", false))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "a thread of a ready pull request does not draft it")
	assert.Equal(t, []string{"report 1   0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AHeadRewrittenWithTheSameTreeIsNotAMove(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.host.pr.same = map[string]bool{"rewrite1": true}
	rig.script("1", rig.view("1", "OPEN", true, "rewrite1"), rig.view("1", "OPEN", false, "rewrite1"), rig.view("1", "MERGED", false, "rewrite1"))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "the rewrite is marked ready and never drafted")
	assert.Equal(t, []string{"report 1   0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AHeadWithAnotherTreeIsAMoveEvenWhenOtherHeadsHoldTheSameTree(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.host.pr.same = map[string]bool{"rewrite1": true}
	rig.script("1", rig.view("1", "OPEN", false, "other1"))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:true"}, rig.host.pr.marked())
	assert.Contains(t, filterReport(rig.h.commands.ran())[0], "1@other1")
}

func TestHandOff_ATreeThatCannotBeReadEndsTheCallAndNamesTheItem(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.host.pr.treeErr = errors.New("no tree")
	rig.script("1", rig.view("1", "OPEN", true, "rewrite1"))

	_, err := rig.walk(t, rig.body("1", ""))

	require.ErrorContains(t, err, "read the pull request of item 1: the tree of the head could not be read")
	assert.Empty(t, rig.host.pr.marked())
}

func TestHandOff_ACancelDuringTheWatchPutsTheReadyPullRequestBackToDraft(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.h.sleep = time.Hour
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false))
	engine := rig.engine(t)
	file := writeFlow(t, bodyFlow(rig.body("1", "")))
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- err
	}()

	waitOn(t, inASleep(t, engine))
	require.NoError(t, rig.h.client(t, testVersion).Cancel(testItem))
	rig.h.tick(t)

	require.Error(t, receive(t, ended))
	assert.Equal(t, []string{"1:false", "1:true"}, rig.host.pr.marked(), "a ready pull request that nobody watches goes back to draft")
}

func TestHandOff_ACancelThatCannotDraftTheReadyPullRequestTellsTheOwnerToConvertByHand(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.h.sleep = time.Hour
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false))
	rig.host.pr.draftErr[true] = errors.New("no permission")
	engine := rig.engine(t)
	file := writeFlow(t, bodyFlow(rig.body("1", "")))
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- err
	}()

	waitOn(t, inASleep(t, engine))
	require.NoError(t, rig.h.client(t, testVersion).Cancel(testItem))
	rig.h.tick(t)

	require.Error(t, receive(t, ended))

	notices := notifyRequests(rig.h)
	require.Len(t, notices, 2, "the notice of the wait and the notice of the draft that failed")
	assert.Contains(t, notices[1].Run[len(notices[1].Run)-1], "cannot make the pull request of item 1 a draft again. If it is open and ready for review, convert it to a draft by hand: no permission")
}

// cancelInTheWatch runs the hand-off until its first sleep, cancels the run and returns the end of the call.
func cancelInTheWatch(t *testing.T, rig *handRig, items string) error {
	t.Helper()

	rig.h.sleep = time.Hour
	engine := rig.engine(t)
	file := writeFlow(t, bodyFlow(rig.body(items, "")))
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- err
	}()

	waitOn(t, inASleep(t, engine))
	require.NoError(t, rig.h.client(t, testVersion).Cancel(testItem))
	rig.h.tick(t)

	return receive(t, ended)
}

func TestHandOff_TheNoticeOfADraftThatFailedRunsOnAContextOfItsOwn(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.h.draft = 50 * time.Millisecond
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false))
	rig.host.pr.blocked = map[string]bool{"1": true}
	// The real runner starts the program with the context that it gets, so a notice on an ended context does not run.
	rig.h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Run[0] == notifyProgram[0] {
			return ctx.Err()
		}

		return nil
	}

	require.Error(t, cancelInTheWatch(t, rig, "1"))

	notices := notifyRequests(rig.h)
	require.Len(t, notices, 2, "the notice of the wait and the notice of the draft that failed, which ran after the limit of the read ended")
	assert.Contains(t, notices[1].Run[len(notices[1].Run)-1], "cannot read the pull request of item 1 to make it a draft again")
}

func TestHandOff_AnOpenPullRequestWithNoHeadIsAReadThatFailed(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true, ""))

	_, err := rig.walk(t, rig.body("1", ""))

	require.ErrorContains(t, err, "read the pull request of item 1: the forge gave no head")
	assert.Empty(t, rig.host.pr.marked(), "a pull request that was not read is not marked ready")
}

func TestHandOff_ACancelDraftsNothingWhenTheForgeGivesNoHeadAndTellsTheOwner(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.view("1", "OPEN", false, ""))

	require.Error(t, cancelInTheWatch(t, rig, "1"))

	notices := notifyRequests(rig.h)
	require.Len(t, notices, 2)
	assert.Contains(t, notices[1].Run[len(notices[1].Run)-1], "cannot read the pull request of item 1 to make it a draft again. If it is open and ready for review, convert it to a draft by hand: the forge gave no head")
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked())
}

func TestHandOff_EachMemberHasItsOwnLimitForTheDraftAfterACancel(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.h.draft = 50 * time.Millisecond
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false))
	rig.script("2", rig.view("2", "OPEN", true), rig.view("2", "OPEN", false))
	rig.host.pr.blocked = map[string]bool{"1": true}

	require.Error(t, cancelInTheWatch(t, rig, "1 2"))

	assert.Contains(t, rig.host.pr.marked(), "2:true", "the read of the first member used all of its limit, and the second member still got its own")
}

func TestHandOff_AWatchSleepsForTheTimeOfTheClockBetweenTwoReads(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.h.sleep = 3 * time.Hour
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false))
	engine := rig.engine(t)
	file := writeFlow(t, bodyFlow(rig.body("1", "")))

	go func() { _, _ = engine.RunFile(testItem, testProject, file, modeOff()) }() //nolint:errcheck // the test cancels the run below

	waitOn(t, inASleep(t, engine))

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	i := slices.IndexFunc(steps, func(s dbos.StepInfo) bool { return s.StepName == "DBOS.sleep" })
	require.GreaterOrEqual(t, i, 0)
	assert.InDelta(t, (3 * time.Hour).Seconds(), steps[i].CompletedAt.Sub(steps[i].StartedAt).Seconds(), 30, "the recorded wake time is the time of the clock after the start of the sleep")

	require.NoError(t, rig.h.client(t, testVersion).Cancel(testItem))
	rig.h.tick(t)
}

func TestHandOff_APullRequestThatIsADraftDuringTheWatchEndsTheMemberAndSaysWhy(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		draft  fakePull
		reason string
	}{
		"with a change request": {fakePull{State: "OPEN", Draft: true, ChangeRequests: 1}, "a review asks for changes"},
		"with a conflict":       {fakePull{State: "OPEN", Draft: true, Conflicting: true}, "it conflicts with its base branch"},
		"drafted by a person":   {fakePull{State: "OPEN", Draft: true}, "it is a draft"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newHandRig(t)
			tc.draft.Head = rig.heads["1"]
			rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), tc.draft)

			_, err := rig.walk(t, rig.body("1", ""))

			require.NoError(t, err)
			assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "a draft in the watch is not marked ready again and not drafted again")
			assert.Equal(t, []string{"report   1:" + tc.reason + " 0"}, filterReport(rig.h.commands.ran()))
		})
	}
}

func TestHandOff_ADraftCallThatWasAppliedButAnsweredWithAnErrorEndsTheMemberOnTheRetry(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	// The third read is the ready pull request with a change request: the draft call fails. The forge applied it, so the retry of the step reads a draft.
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.standing("1", false, func(v *fakePull) { v.ChangeRequests = 1 }), rig.standing("1", true, func(v *fakePull) { v.ChangeRequests = 1 }))
	rig.host.pr.draftErr[true] = errors.New("the answer was lost")

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"report   1:a review asks for changes 0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AResumeAfterACancelThatDraftedTheMemberEndsItAndSaysWhy(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	// Reads: the ready mark, the first round of the watch (the cancel comes with it), the read of the cancel, then the read of the resume, which finds the draft.
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.view("1", "OPEN", false), rig.view("1", "OPEN", true))
	rig.host.pr.onRead = func(_ string, read int) {
		if read != 2 {
			return
		}

		if err := rig.h.client(t, testVersion).Cancel(testItem); err != nil {
			t.Error(err)
		}

		rig.h.tick(t)
	}
	file := writeFlow(t, bodyFlow(rig.body("1", "")))
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file, modeOff())

	require.Error(t, err)
	assert.Equal(t, []string{"1:false", "1:true"}, rig.host.pr.marked(), "the cancel drafts the ready pull request")

	_, err = resumeAndJoin(t, engine, rig.h.client(t, testVersion))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false", "1:true"}, rig.host.pr.marked(), "the resume marks nothing ready and drafts nothing")
	assert.Equal(t, []string{"report   1:it is a draft 0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_ACancelDraftsOnlyTheMembersThatHaveNotEnded(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.h.sleep = time.Hour
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "MERGED", false))
	rig.script("2", rig.view("2", "OPEN", true), rig.view("2", "OPEN", false))
	rig.script("3", rig.standing("3", true, func(v *fakePull) { v.Conflicting = true }))
	engine := rig.engine(t)
	file := writeFlow(t, bodyFlow(rig.body("1 2 3", "")))
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- err
	}()

	waitOn(t, inASleep(t, engine))
	require.NoError(t, rig.h.client(t, testVersion).Cancel(testItem))
	rig.h.tick(t)

	require.Error(t, receive(t, ended))
	assert.Equal(t, []string{"1:false", "2:false", "2:true"}, rig.host.pr.marked(), "the merged member needs no draft call and the other one gets it")
}
