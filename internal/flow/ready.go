package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

// errNotReady is returned by the ready step when the forge shows a reason to hold the pull request back.
var errNotReady = errors.New("the pull request is not ready")

// OpenRequest is what the pr_open step asks of the forge. Issue is the issue number of the item, or 0 for none: the forge adds the closing line for it to the body.
// Parent is the id of the item that this one depends on, or empty.
type OpenRequest struct {
	Title  string
	Body   string
	Issue  int
	Base   string
	Parent string
}

// CardRequest is what the board_sync step asks of the forge. Board is the number of the GitHub Project and Owner is its owner (empty for the owner of the repository).
// Issue is the issue to put on the board, or 0 for the pull request of the item. Status is the name of the column.
type CardRequest struct {
	Board  int
	Owner  string
	Issue  int
	Status string
}

// OpenResult is the pull request that Open found or made. Stack is the id of its native stack, or empty when it is in none.
type OpenResult struct {
	Number  int
	Created bool
	Stack   string
}

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
	// Comment adds a comment to the pull request of the item and returns its number. The same key on the same pull request adds one comment.
	// An item with no pull request is an error. So is a project with no forge.
	Comment(ctx context.Context, project, item, key, text string) (number int, err error)
	// Open finds the open pull request on the branch of the item, or opens a draft one, and makes its body and stack right. The same call again opens no second pull request.
	// A project with no forge, and a forge that cannot open pull requests, are errors.
	Open(ctx context.Context, project, item string, req OpenRequest) (OpenResult, error)
	// PlaceCard puts the card of the pull request of the item (or of the issue in req) on the board and sets its status column, and returns the number of the pull request or issue.
	// The same call again changes nothing. ReadCard reads the status column of the same card from the board, empty when the card has none.
	// A project with no forge, and a forge that cannot write a board, are errors. Both calls refuse a card with no pull request or issue behind it.
	PlaceCard(ctx context.Context, project, item string, req CardRequest) (number int, err error)
	ReadCard(ctx context.Context, project, item string, req CardRequest) (status string, err error)
	// Assign assigns the pull request of the item to a login and returns its number. With linked it also assigns each issue that the pull request closes and returns their numbers.
	// The same call again changes nothing. An item with no pull request is an error. So is a project with no forge.
	Assign(ctx context.Context, project, item, assignee string, linked bool) (number int, issues []int, err error)
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
