package flow

import (
	"context"
	"errors"

	"github.com/hashicorp/hcl/v2"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

// The values of the `state` output of a merge step. While the step waits for the owner, the run shows mergeOwner.
const (
	mergeDone     = "merged"
	mergeSentBack = "sent_back"
	mergeOwner    = "owner_merge"
)

// errMergeNotBuilt is returned when a merge step asks to merge and this build has no merger.
var errMergeNotBuilt = errors.New("merging is not built yet. Remove `merge = true` from the merge step, and the owner merges the pull request")

// runMerge pins the commit in `head`, marks the pull request ready, and merges it only when `merge` is true.
// It then reads the pull request until the owner merges it or sends it back. The wait is a durable sleep.
func runMerge(c *call, step *workflow.Merge) (stepResult, error) {
	head, err := c.text(step.Head)
	if err != nil {
		return stepResult{}, err
	}

	merges, err := c.flag(step.Merge)
	if err != nil {
		return stepResult{}, err
	}

	if merges && c.w.e.deps.Merge == nil {
		return stepResult{}, errMergeNotBuilt
	}

	if err := c.w.readyAndMerge(head, merges); err != nil {
		return stepResult{}, err
	}

	return c.w.awaitOwner(!merges)
}

// readyAndMerge marks the pull request ready at head. It merges the pull request only when merges is true.
func (w *walker) readyAndMerge(head string, merges bool) error {
	ready := func(ctx context.Context, project, item string) error { return w.e.markReady(ctx, project, item, head) }
	if err := w.e.lifecycle(w.ctx, model.StepReady, ready, w.in.Project, w.in.Item); err != nil {
		return err
	}

	if !merges {
		return nil
	}

	return w.e.mergeReady(w.ctx, w.in.Project, w.in.Item, head)
}

// awaitOwner reads the pull request until the owner merges it or sends it back. When ownerMerges is true, the run first shows that it waits for the owner.
// The state and the reason go into the result of the item. The reason is empty after a merge.
func (w *walker) awaitOwner(ownerMerges bool) (stepResult, error) {
	reason, err := w.watchOwner(ownerMerges)
	if err != nil {
		return stepResult{}, err
	}

	state := mergeDone
	if reason != "" {
		state = mergeSentBack
	}

	w.merge, w.sentBack = state, reason

	return stepResult{Outputs: map[string]any{"state": state, "reason": reason}}, nil
}

// watchOwner shows that the run waits for the owner when ownerMerges is true, then watches the pull request.
func (w *walker) watchOwner(ownerMerges bool) (string, error) {
	var err error

	if ownerMerges {
		err = show(w.ctx, w.in.Item, mergeKey, mergeOwner, "that it waits for the owner to merge")
	}

	var reason string

	if err == nil {
		reason, err = w.e.watchReady(w.ctx, w.in.Project, w.in.Item)
	}

	return reason, err
}

// flag evaluates an optional argument that is true or false. An absent argument is false.
func (c *call) flag(expr hcl.Expression) (bool, error) {
	if absent(expr) {
		return false, nil
	}

	return c.w.scope.Bool(expr, c.round)
}
