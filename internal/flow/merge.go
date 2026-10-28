package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/merge"
)

// mergeStep is the name of the step that merges the ready pull request.
const mergeStep = "merge"

// errMergeRefused is returned when the merge refuses a pull request for a reason other than its owner: the owner has to act.
var errMergeRefused = errors.New("the merge was refused")

// merger merges the pull request of an item, pinned to head. It returns the verdict of the merge with the lines to show.
// A pull request of ours is always refused with merge.OwnWork: the owner merges it in the forge.
type merger interface {
	Merge(ctx context.Context, project, item, head string) (merge.Result, error)
}

// mergeReady merges the ready pull request, pinned to the head that the gate and the review read.
// A merge that was done, or that was refused as our own work, goes on to the watch, which ends when the owner merges. Any other refusal ends the item.
// The caller makes sure that the build has a merger.
func (e *Engine) mergeReady(ctx dbos.Context, project, item, head string) error {
	result, err := runStep(e, ctx, mergeStep, func(c context.Context) (merge.Result, error) {
		return e.deps.Merge.Merge(c, project, item, head)
	})
	if err != nil {
		return fmt.Errorf("%s of item %s: %w", mergeStep, item, err)
	}

	switch result.Stop {
	case merge.Done, merge.OwnWork:
		return nil
	case merge.Refused, merge.NoWorkflowScope:
	}

	return fmt.Errorf("%w: item %s: %s", errMergeRefused, item, strings.Join(result.Lines, "; "))
}
