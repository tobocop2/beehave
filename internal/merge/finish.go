package merge

import (
	"fmt"
	"strings"
)

// conclude readies a draft the owner allowed, reads the head once more, then merges pinned to the gated head and shows the tip.
// The head is read again because a push can land between the guards and the merge. The merge call pins the head on the forge side.
func (a *attempt) conclude() *refusal {
	for _, step := range []func() *refusal{a.ready, a.unmoved, a.nativeStack} {
		if stop := step(); stop != nil {
			return stop
		}
	}

	if err := a.forge.MergePR(a.ctx, a.req.PR, a.req.Gated); err != nil {
		return refuse("REFUSE: " + err.Error())
	}

	a.merged()

	return nil
}

// unmoved refuses a pull request whose head moved since the gate.
func (a *attempt) unmoved() *refusal {
	again, err := a.forge.PRView(a.ctx, a.req.PR, "headRefOid")
	if err != nil {
		return refuse(fmt.Sprintf("REFUSE: cannot read the head of #%d again", a.req.PR))
	}

	if head := text(again.HeadRefOid); head != a.req.Gated {
		return refuse(fmt.Sprintf("REFUSE: head moved to %s while preparing. Re-gate.", head))
	}

	return nil
}

// nativeStack refuses a pull request in a native stack, which merges through the asynchronous merge that obee does not run.
func (a *attempt) nativeStack() *refusal {
	stack, err := a.forge.StackID(a.ctx, a.req.PR)
	if err != nil {
		return refuse(fmt.Sprintf("REFUSE: cannot read which stack #%d is in", a.req.PR))
	}

	if stack != "" {
		return refuse(fmt.Sprintf("REFUSE: #%d is in stack %s. A pull request in a native stack merges through the asynchronous merge, which obee does not run.", a.req.PR, stack))
	}

	return nil
}

// ready marks a draft ready and reads it again. A pull request that stays a draft refuses. A pull request that is not a draft needs nothing.
func (a *attempt) ready() *refusal {
	pr := a.req.PR
	if !*a.view.IsDraft {
		return nil
	}

	if err := a.forge.SetDraft(a.ctx, pr, false); err != nil {
		return refuse(fmt.Sprintf("REFUSE: cannot mark #%d ready: %s", pr, err))
	}

	now, err := a.forge.PRView(a.ctx, pr, "isDraft")
	if err != nil {
		return refuse(fmt.Sprintf("REFUSE: cannot read #%d after it was marked ready", pr))
	}

	if now.IsDraft == nil || *now.IsDraft {
		return refuse(fmt.Sprintf("REFUSE: #%d is still a draft after it was marked ready", pr))
	}

	return nil
}

// merged says the merge and the tip of the base after it. A tip that cannot be read does not undo the merge.
func (a *attempt) merged() {
	base := a.req.Settings.Base()

	tip, found, err := a.forge.BranchRef(a.ctx, base)
	if err != nil || !found {
		tip = "unreadable"
	}

	a.say(fmt.Sprintf("MERGED #%d at %s; %s tip is now %s", a.req.PR, a.req.Gated, base, tip))

	if a.req.IntegrationPR != 0 {
		a.record()
	}
}

// record adds the row of the merge to the integration pull request, under the lock of its body. A row that cannot be written does not undo the merge.
func (a *attempt) record() {
	row := fmt.Sprintf("- #%d merged at %s", a.req.PR, a.req.Gated)
	add := func(body string) string { return strings.TrimRight(body, "\n") + "\n" + row }

	if err := EditBody(a.ctx, a.req.Dir, a.forge, a.req.IntegrationPR, add); err != nil {
		a.say(fmt.Sprintf("integration-row: not recorded on #%d: %s. Add the row by hand.", a.req.IntegrationPR, err))

		return
	}

	a.say(fmt.Sprintf("integration-row: recorded on #%d", a.req.IntegrationPR))
}
