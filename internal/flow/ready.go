package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

// errNotReady is returned by the ready step when the forge shows a reason to hold the pull request back.
var errNotReady = errors.New("the pull request is not ready")

// forge reads the pull request of an item and takes it out of draft. A failed read is an error, never an empty standing.
// Ready runs after an approved review and a clean Pull.
// Reviewed reads the same standing as Pull for a review of the commit reviewed. A head that is not that commit but holds the same tree
// is a rewrite, and the review stands for it (Standing.Rewritten).
type forge interface {
	Pull(ctx context.Context, project, item string) (review.Standing, error)
	Reviewed(ctx context.Context, project, item, reviewed string) (review.Standing, error)
	Ready(ctx context.Context, project, item string) error
	// ClosingIssues reads the issues that merging the pull request of the item closes, as the forge lists them. Found is false when the item has no pull request yet.
	ClosingIssues(ctx context.Context, project, item string) (issues []int, found bool, err error)
}

// markReady reads the forge and refuses a pull request with a reason to wait, or one that moved after the review to a head with another tree.
// Only a clean read lets the lifecycle mark the pull request ready. It returns the head that the merge pins: the head of the pull request,
// which is the reviewed commit or a rewrite of it with the same tree.
func (e *Engine) markReady(ctx context.Context, project, item, reviewed string) (string, error) {
	standing, err := e.deps.Forge.Reviewed(ctx, project, item, reviewed)
	if err != nil {
		return "", fmt.Errorf("read the pull request of item %s: %w", item, err)
	}

	if reasons := standing.Reasons(reviewed); len(reasons) > 0 {
		return "", fmt.Errorf("%w: pull request %d: %s", errNotReady, standing.Number, strings.Join(reasons, ", "))
	}

	if err := e.deps.Forge.Ready(ctx, project, item); err != nil {
		return "", err
	}

	return standing.Head, e.holdHead(ctx, project, item, standing)
}

// holdHead reads the pull request again right after the ready call. The ready call cannot name a head, so a push between the standing read and the call
// would leave a head that nobody reviewed marked ready. A head that moved, or one that cannot be read, puts the pull request back to draft.
func (e *Engine) holdHead(ctx context.Context, project, item string, read review.Standing) error {
	now, err := e.deps.Forge.Pull(ctx, project, item)
	if err == nil && now.Head == read.Head {
		return nil
	}

	if draftErr := e.deps.Watch.SetDraft(ctx, project, read.Number, true); draftErr != nil {
		return errors.Join(fmt.Errorf("put pull request %d back to draft: %w", read.Number, draftErr), err)
	}

	if err != nil {
		return fmt.Errorf("read the head of pull request %d after it was marked ready: %w", read.Number, err)
	}

	return fmt.Errorf("%w: pull request %d: it moved to %q while it was marked ready", errNotReady, read.Number, now.Head)
}

// readyStep runs markReady as the recorded step of the lifecycle. A replay reads the recorded head, so the merge pins the same head after a restart.
func (e *Engine) readyStep(ctx dbos.Context, project, item, reviewed string) (string, error) {
	pinned, err := runStep(e, ctx, string(model.StepReady), func(c context.Context) (string, error) {
		return e.markReady(c, project, item, reviewed)
	})
	if err != nil {
		return "", fmt.Errorf("%s of item %s: %w", model.StepReady, item, err)
	}

	return pinned, nil
}
