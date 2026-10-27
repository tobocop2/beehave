package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tobocop2/obee/internal/review"
)

// errNotReady is returned by the ready step when the forge shows a reason to hold the pull request back.
var errNotReady = errors.New("the pull request is not ready")

// forge reads the pull request of an item. A failed read is an error, never an empty standing.
type forge interface {
	Pull(ctx context.Context, project, item string) (review.Standing, error)
}

// markReady reads the forge and refuses a pull request with a reason to wait, or one that moved after the review.
// Only a clean read lets the lifecycle mark the pull request ready.
func (e *Engine) markReady(ctx context.Context, project, item, reviewed string) error {
	standing, err := e.deps.Forge.Pull(ctx, project, item)
	if err != nil {
		return fmt.Errorf("read the pull request of item %s: %w", item, err)
	}

	if reasons := standing.Reasons(reviewed); len(reasons) > 0 {
		return fmt.Errorf("%w: pull request %d: %s", errNotReady, standing.Number, strings.Join(reasons, ", "))
	}

	return e.deps.Lifecycle.Ready(ctx, project, item)
}
