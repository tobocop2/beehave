package merge

import (
	"context"
	"fmt"

	"github.com/tobocop2/obee/internal/model"
)

// queued asks the forge to merge a pull request in a native stack. The forge owns the merge: it holds the queued merge and merges when its requirements pass.
// The run only queues the merge and returns Queued. The caller waits and reads the pull request with Poll, as recorded steps with a durable sleep between them.
// A repeated call is safe: the forge keeps one queued merge for a pull request.
func (a *attempt) queued() *refusal {
	if err := a.queue.AutoMergePR(a.ctx, a.req.PR, a.req.Gated); err != nil {
		return transient("REFUSE: " + err.Error())
	}

	return &refusal{stop: Refused, reason: Queued, lines: []string{fmt.Sprintf("queued the merge of #%d at %s on the forge", a.req.PR, a.req.Gated)}}
}

// Poll reads a pull request whose merge is queued on the forge, once, and judges it. The pin of the queued merge is checked only when the forge enables it,
// and a push by a user with write access keeps it armed. So every read compares the head with the gated head, and a head that moved disarms the queued merge.
//
// The result is Queued while the pull request is open at the gated head: read again later. Last is true for the final read of the wait: a pull request that is still open
// then is disarmed, and the result is Transient. A MERGED pull request at the gated head moves its children when the request says so, then goes to the ancestor and tree proofs.
// A MERGED pull request at any other head is a refusal that says commits were merged that no review saw.
func Poll(ctx context.Context, forge Forge, req Request, last bool) Result {
	run := &attempt{ctx: ctx, forge: forge, req: req}

	queue, ok := forge.(Queuer)
	if !ok {
		return Result{Stop: Refused, Lines: []string{"REFUSE: the forge cannot queue a merge, so it cannot read one."}}
	}

	run.queue = queue

	stop := run.poll(last)
	if stop == nil {
		return Result{Stop: Done, Lines: run.lines}
	}

	return Result{Stop: stop.stop, Reason: stop.reason, Lines: append(run.lines, stop.lines...)}
}

// poll reads the pull request and decides. The merged check comes first: a merge that happened cannot be disarmed.
func (a *attempt) poll(last bool) *refusal {
	view, err := a.forge.PRView(a.ctx, a.req.PR, "state", "headRefOid", "headRefName")
	if err != nil {
		return a.unread(err, last)
	}

	a.view = view
	head, state := model.Deref(view.HeadRefOid), model.Deref(view.State)

	switch {
	case state == stateMerged:
		return a.mergedQueued(head)
	case head != a.req.Gated:
		return a.disarmed(refuse(fmt.Sprintf("REFUSE: head moved to %s while the merge of #%d was queued. Re-gate.", head, a.req.PR)))
	case state != stateOpen:
		return a.disarmed(refuse(fmt.Sprintf("REFUSE: #%d is %s now, so its queued merge cannot land.", a.req.PR, state)))
	case last:
		return a.disarmed(transient(fmt.Sprintf("REFUSE: #%d was not merged within the wait. Run the merge again.", a.req.PR)))
	}

	return &refusal{stop: Refused, reason: Queued, lines: []string{fmt.Sprintf("#%d is queued at %s and not merged yet", a.req.PR, a.req.Gated)}}
}

// unread is the result of a read that failed. The queued merge is still on the forge, so the wait goes on until the last read, which disarms it.
func (a *attempt) unread(err error, last bool) *refusal {
	stop := transient(fmt.Sprintf("REFUSE: cannot read #%d while its queued merge runs: %s", a.req.PR, err))
	if last {
		return a.disarmed(stop)
	}

	return &refusal{stop: Refused, reason: Queued, lines: stop.lines}
}

// mergedQueued judges a pull request that the forge merged. The merged head must be the gated head: the forge does not check the pin when it merges.
func (a *attempt) mergedQueued(head string) *refusal {
	if head != a.req.Gated {
		return refuse(fmt.Sprintf("REFUSE: #%d was merged at %s, not at the gated %s. The forge merged commits that no review saw. Check %s.", a.req.PR, head, a.req.Gated, a.req.Base))
	}

	if stop := a.provable(); stop != nil {
		return stop
	}

	if stop := a.retargetMerged(); stop != nil {
		return stop
	}

	return a.landed(a.merged())
}

// retargetMerged moves the children of a pull request that is merged onto the base. A request that does not retarget moves none.
func (a *attempt) retargetMerged() *refusal {
	if !a.req.Retarget {
		return nil
	}

	kids, stop := a.children()
	if stop != nil {
		return stop
	}

	a.kids = kids

	return a.retarget()
}

// disarmed turns off the queued merge on the forge, then returns the refusal. A refusal must not leave a merge armed that can land later.
// If the forge cannot turn it off, the refusal says so and is not transient: the owner has to disarm it.
func (a *attempt) disarmed(stop *refusal) *refusal {
	if err := a.queue.DisableAutoMergePR(a.ctx, a.req.PR); err != nil {
		return refuse(append(stop.lines, fmt.Sprintf("REFUSE: the queued merge of #%d may still be armed: %s. Turn it off by hand.", a.req.PR, err))...)
	}

	stop.lines = append(stop.lines, fmt.Sprintf("the queued merge of #%d is turned off", a.req.PR))

	return stop
}

// Disarm turns off the queued merge of a pull request whose wait ended with no verdict: an owner cancel or an engine error. Poll disarms on every verdict it gives,
// so only a wait that leaves without a verdict calls this. A pull request that the forge already merged has nothing to turn off, and the call does not touch it.
// A read that fails does not stop the disarm: the merge may be armed, and turning it off is safe when none is. If the pull request was in fact merged and
// the forge refuses to turn off a merge on it, the caller gets a refusal for a pull request that landed. The result is Done when no merge stays armed.
func Disarm(ctx context.Context, forge Forge, req Request) Result {
	queue, ok := forge.(Queuer)
	if !ok {
		return Result{Stop: Refused, Lines: []string{"REFUSE: the forge cannot queue a merge, so it cannot turn one off."}}
	}

	run := &attempt{ctx: ctx, forge: forge, req: req, queue: queue}

	if view, err := forge.PRView(ctx, req.PR, "state", "headRefOid", "headRefName"); err == nil && model.Deref(view.State) == stateMerged {
		return Result{Stop: Done, Lines: []string{fmt.Sprintf("#%d is merged, so no queued merge is armed", req.PR)}}
	}

	stop := run.disarmed(&refusal{stop: Done})

	return Result{Stop: stop.stop, Reason: stop.reason, Lines: stop.lines}
}
