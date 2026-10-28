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

// Activity is what is said on a pull request and what is done to it. Every method but the two reads changes the forge.
// MergePR merges only if the head of the pull request is still sha, a full 40-character commit id. A shorter pin is refused.
// CreateBranch makes a branch at sha and fails if the branch exists. SetBase retargets a pull request. SetBody replaces its body.
type Activity interface {
	Comments(ctx context.Context, number int) ([]model.Comment, error)
	ReviewComments(ctx context.Context, pr int) ([]model.Comment, error)
	PostComment(ctx context.Context, pr int, body string) error
	SetDraft(ctx context.Context, pr int, draft bool) error
	MergePR(ctx context.Context, pr int, sha string) error
	CreateBranch(ctx context.Context, branch, sha string) error
	SetBase(ctx context.Context, pr int, base string) error
	SetBody(ctx context.Context, pr int, body string) error
}

// Lookup finds items and branches: a search, the default branch and the tip of a branch.
// TokenScopes lists the scopes of the token the forge command line uses.
// PullCounts returns the forge's own count of the commits and changed files of a pull request. A list in PRView can be cut short, so a caller compares the count to the list.
type Lookup interface {
	PullCounts(ctx context.Context, pr int) (commits, files int, err error)
	TokenScopes(ctx context.Context) ([]string, error)
	Search(ctx context.Context, req model.SearchRequest) ([]int, error)
	DefaultBranch(ctx context.Context) (string, error)
	BranchRef(ctx context.Context, branch string) (sha string, found bool, err error)
	Title(ctx context.Context, number int) (string, error)
	CheckNotStarted(ctx context.Context, link string) (bool, error)
}
