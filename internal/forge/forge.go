// Package forge holds the contract of a code host, with its errors. Each backend is a child package.
package forge

import (
	"context"

	"github.com/tobocop2/obee/internal/model"
)

// Forge is a code host: pull requests, reviews, threads and checks. GitHub implements it.
// A failed command is an error. Only BranchRef tells a missing branch (found false, no error) from a failure.
type Forge interface {
	PullReads
	Activity
	Lookup
	Standing
}

// Standing is the review state of our own open pull requests, read in one call.
type Standing interface {
	OwnPulls(ctx context.Context, author string) ([]model.OwnPull, error)
}

// PullReads are the reads of one pull request: its reviews, body, fields, checks, threads and overlap.
type PullReads interface {
	Reviews(ctx context.Context, pr int) ([]model.Review, error)
	PRBody(ctx context.Context, pr int) (string, error)
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error)
	PRChecks(ctx context.Context, pr int) ([]model.Check, error)
	OpenChangeRequests(ctx context.Context, pr int) ([]model.ChangeRequest, error)
	UnresolvedThreads(ctx context.Context, pr int) ([]model.ReviewThread, error)
	StackID(ctx context.Context, pr int) (string, error)
	ExistingReview(ctx context.Context, pr int) ([]string, error)
	Overlap(ctx context.Context, req model.OverlapRequest) (model.Overlap, error)
}

// Activity is what is said on a pull request and what is done to it. PostComment and SetDraft change the forge.
type Activity interface {
	Comments(ctx context.Context, number int) ([]model.Comment, error)
	ReviewComments(ctx context.Context, pr int) ([]model.Comment, error)
	PostComment(ctx context.Context, pr int, body string) error
	SetDraft(ctx context.Context, pr int, draft bool) error
}

// Lookup finds items and branches: a search, the default branch and the tip of a branch.
type Lookup interface {
	Search(ctx context.Context, req model.SearchRequest) ([]int, error)
	DefaultBranch(ctx context.Context) (string, error)
	BranchRef(ctx context.Context, branch string) (sha string, found bool, err error)
	Title(ctx context.Context, number int) (string, error)
	CheckNotStarted(ctx context.Context, link string) (bool, error)
}
