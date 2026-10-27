// Package forge holds the contract of a code host, with its errors. Each backend is a child package.
package forge

import (
	"context"

	"github.com/tobocop2/obee/internal/model"
)

// Forge is a code host: pull requests, reviews, threads and checks. GitHub implements it.
type Forge interface {
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
