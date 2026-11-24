package flow

import (
	"context"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

// OpenRequest is what the pr_open step asks of the forge. Issue is the issue number of the item, or 0 for none: the forge adds the closing line for it to the body.
// Parent is the id of the item that this one depends on, or empty.
type OpenRequest struct {
	Title  string
	Body   string
	Issue  int
	Base   string
	Parent string
}

// OpenResult is the pull request that Open found or made. Stack is the id of its native stack, or empty when it is in none.
type OpenResult struct {
	Number  int
	Created bool
	Stack   string
}

// forge reads and writes the pull request of an item. A failed read is an error, never an empty standing.
type forge interface {
	Pull(ctx context.Context, project, item string) (review.Standing, error)
	// ClosingIssues reads the issues that merging the pull request of the item closes, as the forge lists them: "#N" for an issue of the repository of the pull request,
	// "owner/repo#N" for an issue of another repository. Found is false when the item has no pull request yet. A project with no forge is an error that matches forge.ErrNoForge.
	ClosingIssues(ctx context.Context, project, item string) (issues []model.IssueRef, found bool, err error)
	// Comment adds a comment to the pull request of the item and returns its number. The same key on the same pull request adds one comment.
	// An item with no pull request is an error. So is a project with no forge.
	Comment(ctx context.Context, project, item, key, text string) (number int, err error)
	// Open finds the open pull request on the branch of the item, or opens a draft one, and makes its body and stack right. The same call again opens no second pull request.
	// A project with no forge, and a forge that cannot open pull requests, are errors.
	Open(ctx context.Context, project, item string, req OpenRequest) (OpenResult, error)
	// Push pushes the commit head of the clone of the project to the branch on the remote that the engine read, with no setting from the git config of the
	// clone. expect is the lease: the head the branch must hold, or empty for a new branch or a fast forward only. A branch already at head is no error, so a
	// replay passes. A branch that moved is an error that names it. A project with no forge or no clone is an error.
	Push(ctx context.Context, project, item, branch, head, expect string) error
	// History lists the pull requests of the item in every state, each closed or merged one with its closing comment. A project with no forge has none.
	History(ctx context.Context, project, item string) ([]model.PriorPull, error)
}
