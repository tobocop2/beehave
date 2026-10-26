package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// errNotReady is returned by the ready step when the forge shows a reason to hold the pull request back.
var errNotReady = errors.New("the pull request is not ready")

// PullState is what the ready step reads from the forge about the pull request of an item.
type PullState struct {
	Number            int
	ChangesRequested  bool
	UnresolvedThreads int
	Conflicting       bool
	// Head is the commit the pull request shows now.
	Head string
}

// forge reads the pull request of an item. A failed read is an error, never an empty state.
type forge interface {
	Pull(ctx context.Context, project, item string) (PullState, error)
}

// reasons lists why the pull request is not ready, in the order the ready step checks them. reviewed is the commit the review read.
func (s PullState) reasons(reviewed string) []string {
	var reasons []string

	if s.Head != reviewed {
		reasons = append(reasons, fmt.Sprintf("it moved to %q after the review read %q", s.Head, reviewed))
	}

	if s.ChangesRequested {
		reasons = append(reasons, "a review asks for changes")
	}

	if s.UnresolvedThreads > 0 {
		reasons = append(reasons, fmt.Sprintf("%d review threads are unresolved", s.UnresolvedThreads))
	}

	if s.Conflicting {
		reasons = append(reasons, "it conflicts with its base branch")
	}

	return reasons
}

// markReady reads the forge and refuses a pull request with a reason to wait, or one that moved after the review.
// Only a clean read lets the lifecycle mark the pull request ready.
func (e *Engine) markReady(ctx context.Context, project, item, reviewed string) error {
	state, err := e.deps.Forge.Pull(ctx, project, item)
	if err != nil {
		return fmt.Errorf("read the pull request of item %s: %w", item, err)
	}

	if reasons := state.reasons(reviewed); len(reasons) > 0 {
		return fmt.Errorf("%w: pull request %d: %s", errNotReady, state.Number, strings.Join(reasons, ", "))
	}

	return e.deps.Lifecycle.Ready(ctx, project, item)
}
