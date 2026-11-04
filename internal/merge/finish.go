package merge

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// conclude readies a draft the owner allowed, reads the head and the base tip once more, moves the children, then merges pinned to the gated head and shows the tip.
// The head and the tip are read again because a push can land between the guards and the merge. The merge call pins the head on the forge side.
// A pull request that the intent shows merged by an earlier run of this step skips all of that and goes to the tree proof.
// The children move last: a pull request that any guard refuses leaves its children where they are.
// A pull request in a native stack is queued on the forge and returns Queued: its children move in Poll, after the forge has merged it.
// A replay of a merge that the forge made moves the children that are still on the branch, then goes to the proofs.
func (a *attempt) conclude() *refusal {
	if a.replayed() {
		if stop := a.retargetMerged(); stop != nil {
			return stop
		}

		return a.landed(a.merged())
	}

	for _, step := range []func() *refusal{a.ready, a.unmoved, a.nativeStack, a.mergeNow} {
		if stop := step(); stop != nil {
			return stop
		}
	}

	return a.landed(a.merged())
}

// mergeNow queues the merge of a pull request in a native stack and returns Queued, which ends the run. Any other pull request moves its children, then merges directly.
func (a *attempt) mergeNow() *refusal {
	if a.queue != nil {
		return a.queued()
	}

	if stop := a.retarget(); stop != nil {
		return stop
	}

	return a.mergeCall()
}

// mergeCall merges the pull request directly, pinned to the gated head.
func (a *attempt) mergeCall() *refusal {
	if err := a.forge.MergePR(a.ctx, a.req.PR, a.req.Gated); err != nil {
		return transient("REFUSE: " + err.Error())
	}

	return nil
}

// landed proves the merge. The merged head must be an ancestor of the base, then the tree proof runs. The merge already happened, so a stop here leaves the lines of the merge in front of it.
func (a *attempt) landed(tip string) *refusal {
	if stop := a.ancestor(); stop != nil {
		return stop
	}

	return a.tipTree(tip)
}

// provable refuses, before any write, a forge that cannot compare commits. The merge cannot be undone, so the landing must be provable first.
func (a *attempt) provable() *refusal {
	history, ok := a.forge.(Ancestry)
	if !ok {
		return refuse(fmt.Sprintf("REFUSE: the forge cannot compare commits, so it cannot prove that the merge of #%d landed on %s. Nothing was changed.", a.req.PR, a.req.Base))
	}

	a.history = history

	return nil
}

// ancestor stops when the merged head is not in the history of the base, or when that cannot be told.
func (a *attempt) ancestor() *refusal {
	in, err := a.history.IsAncestor(a.ctx, a.req.Gated, a.req.Base)
	if err != nil {
		return stopped(fmt.Sprintf("cannot tell whether %s is in the history of %s: %s", a.req.Gated, a.req.Base, err))
	}

	if !in {
		return stopped(fmt.Sprintf("the merged head %s is not on %s, so #%d did not land.", a.req.Gated, a.req.Base, a.req.PR))
	}

	return nil
}

// unmoved refuses a pull request that is no longer open, or is a draft, or whose head moved since the gate, and a base whose tip moved since the start of the run.
// It reads the pull request again right before the merge call.
func (a *attempt) unmoved() *refusal {
	again, err := a.forge.PRView(a.ctx, a.req.PR, "headRefOid", "state", "isDraft")
	if err != nil {
		return transient(fmt.Sprintf("REFUSE: cannot read the head of #%d again", a.req.PR))
	}

	if stop := a.unchanged(again); stop != nil {
		return stop
	}

	tip, stop := a.baseTip()
	if stop != nil {
		return stop
	}

	if tip != a.tip {
		return transient(fmt.Sprintf("REFUSE: %s moved from %s to %s while preparing. Merge the new tip into the branch and gate it again.", a.req.Base, a.tip, tip))
	}

	return nil
}

// unchanged refuses a read whose head is not the gated head, whose state is not open, or which is a draft.
func (a *attempt) unchanged(again model.PullRequest) *refusal {
	if head := model.Deref(again.HeadRefOid); head != a.req.Gated {
		return refuse(fmt.Sprintf("REFUSE: head moved to %s while preparing. Re-gate.", head))
	}

	if stop := a.stillOpen(again); stop != nil {
		return stop
	}

	if again.IsDraft != nil && *again.IsDraft {
		return refuse(fmt.Sprintf("REFUSE: #%d turned into a draft while preparing.", a.req.PR))
	}

	return nil
}

// stillOpen refuses a read whose state is not OPEN. A state the read does not hold counts as not open: an unknown state is not a passing one.
func (a *attempt) stillOpen(view model.PullRequest) *refusal {
	if state := model.Deref(view.State); state != stateOpen {
		return refuse(fmt.Sprintf("REFUSE: #%d is %s now, so nothing more is done to it.", a.req.PR, state))
	}

	return nil
}

// start reads the tip of the base. The run compares it with the tip right before the merge call.
func (a *attempt) start() *refusal {
	tip, stop := a.baseTip()
	a.tip = tip

	return stop
}

// baseTip reads the tip of the base. A base that cannot be read refuses: an unreadable guard is not a passing one.
func (a *attempt) baseTip() (string, *refusal) {
	tip, found, err := a.forge.BranchRef(a.ctx, a.req.Base)
	if err != nil || !found {
		return "", transient(fmt.Sprintf("REFUSE: cannot read the tip of %s, so a move of the base cannot be ruled out", a.req.Base))
	}

	return tip, nil
}

// nativeStack decides how a pull request in a native stack merges. The forge merges it asynchronously, so the run queues the merge on the forge
// when the request allows it and the forge can queue a merge. Otherwise the pull request is refused.
func (a *attempt) nativeStack() *refusal {
	stack, err := a.forge.StackID(a.ctx, a.req.PR)
	if err != nil {
		return refuse(fmt.Sprintf("REFUSE: cannot read which stack #%d is in", a.req.PR))
	}

	if stack == "" {
		return nil
	}

	if queue, ok := a.forge.(Queuer); ok && a.req.Queue {
		a.queue = queue

		return nil
	}

	return refuse(fmt.Sprintf("REFUSE: #%d is in stack %s. A pull request in a native stack merges through the asynchronous merge of the forge. This run does not queue merges, or the forge cannot queue a merge.", a.req.PR, stack))
}

// ready marks a draft ready and reads it again. A pull request that stays a draft refuses. A pull request that is not a draft needs nothing.
func (a *attempt) ready() *refusal {
	pr := a.req.PR
	if !*a.view.IsDraft {
		return nil
	}

	if stop := a.openBeforeReady(); stop != nil {
		return stop
	}

	if err := a.forge.SetDraft(a.ctx, pr, false); err != nil {
		return refuse(fmt.Sprintf("REFUSE: cannot mark #%d ready: %s", pr, err))
	}

	return a.readyNow()
}

// readyNow reads the pull request after it was marked ready. A pull request that stays a draft refuses.
func (a *attempt) readyNow() *refusal {
	now, err := a.forge.PRView(a.ctx, a.req.PR, "isDraft")
	if err != nil {
		return refuse(fmt.Sprintf("REFUSE: cannot read #%d after it was marked ready", a.req.PR))
	}

	if now.IsDraft == nil || *now.IsDraft {
		return refuse(fmt.Sprintf("REFUSE: #%d is still a draft after it was marked ready", a.req.PR))
	}

	return nil
}

// openBeforeReady reads the state of the pull request again right before it is marked ready.
func (a *attempt) openBeforeReady() *refusal {
	before, err := a.forge.PRView(a.ctx, a.req.PR, "state")
	if err != nil {
		return transient(fmt.Sprintf("REFUSE: cannot read the state of #%d before it is marked ready", a.req.PR))
	}

	return a.stillOpen(before)
}

// merged says the merge and the tip of the base after it, and returns the tip. A tip that cannot be read does not undo the merge: the result is empty.
func (a *attempt) merged() string {
	base := a.req.Base

	tip, found, err := a.forge.BranchRef(a.ctx, base)
	shown := tip

	if err != nil || !found {
		tip, shown = "", "unreadable"
	}

	a.say(fmt.Sprintf("MERGED #%d at %s; %s tip is now %s", a.req.PR, a.req.Gated, base, shown))

	if a.req.IntegrationPR != 0 {
		a.record()
	}

	return tip
}

// writesOwnBody reads the author of the integration pull request and says whether the owner wrote it. A body of another author, or of an author that cannot be read, is never edited. The merge stays done, and the step says that the row is for the owner to add.
func (a *attempt) writesOwnBody() bool {
	view, err := a.forge.PRView(a.ctx, a.req.IntegrationPR, "author")
	if err != nil {
		a.say(fmt.Sprintf("REFUSE: integration-row not recorded on #%d: cannot read its author: %v. Add the row by hand.", a.req.IntegrationPR, err))

		return false
	}

	if !IsOwn(view.Author.Login, a.req.OwnLogins) {
		a.say(fmt.Sprintf("REFUSE: integration-row not recorded on #%d: its author is %q, not the owner. Add the row by hand.", a.req.IntegrationPR, view.Author.Login))

		return false
	}

	return true
}

// record adds the row of the merge to the integration pull request, under the lock of its body. A body that already holds the exact row, on a line of its own with either line ending, stays as it is, so a replay writes the row once. A row that cannot be written does not undo the merge.
func (a *attempt) record() {
	row := fmt.Sprintf("- #%d merged at %s", a.req.PR, a.req.Gated)
	add := func(body string) string {
		if slices.Contains(strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n"), row) {
			return body
		}

		return strings.TrimRight(body, "\n") + "\n" + row
	}

	if !a.writesOwnBody() {
		return
	}

	if err := EditBody(a.ctx, a.req.Dir, a.forge, a.req.IntegrationPR, add); err != nil {
		a.say(fmt.Sprintf("integration-row: not recorded on #%d: %s. Add the row by hand.", a.req.IntegrationPR, err))

		return
	}

	a.say(fmt.Sprintf("integration-row: recorded on #%d", a.req.IntegrationPR))
}
