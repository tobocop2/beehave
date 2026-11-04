// Package review says what review work waits: the poll of items to review, answer or fix, and the sweep of our own pull requests.
// An unread list is not an empty one: a failed read stops the poll with an UnreadError, which the command line maps to exit code 2.
package review

import (
	"context"
	"fmt"

	"github.com/tobocop2/beehave/internal/model"
)

// The limits of the poll. gh caps an unlimited list at 30 without a word, so every list names its limit.
const (
	ListLimit   = 1000
	SearchLimit = 200
	RecentDays  = 3
)

const (
	titleWidth      = 60
	issueTitleWidth = 70
	headWidth       = 9
	updatedWidth    = 16
	none            = "-"
	noReview        = "NONE"
)

// Reader is the forge reads of the poll. The forge backend implements it.
type Reader interface {
	PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error)
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	Reviews(ctx context.Context, pr int) ([]model.Review, error)
	Comments(ctx context.Context, number int) ([]model.Comment, error)
	ReviewComments(ctx context.Context, pr int) ([]model.Comment, error)
	Search(ctx context.Context, req model.SearchRequest) ([]int, error)
	PRChecks(ctx context.Context, pr int) ([]model.Check, error)
	Title(ctx context.Context, number int) (string, error)
	CheckNotStarted(ctx context.Context, link string) (bool, error)
}

// Own reads the review state of our open pull requests in one call.
type Own interface {
	OwnPulls(ctx context.Context, author string) ([]model.OwnPull, error)
}

// Stacks reads what the sweep needs to explain a base branch.
type Stacks interface {
	StackID(ctx context.Context, pr int) (string, error)
	DefaultBranch(ctx context.Context) (string, error)
}

// UnreadError is a read that failed. The poll stops with it, and the command line exits with code 2.
type UnreadError struct {
	Message string
	Err     error
}

func (e *UnreadError) Error() string {
	if e.Err == nil {
		return "review-poll: " + e.Message
	}

	return fmt.Sprintf("%s\nreview-poll: %s", e.Err, e.Message)
}

// Unwrap returns the read that failed.
func (e *UnreadError) Unwrap() error { return e.Err }

func unread(err error, format string, args ...any) error {
	return &UnreadError{Message: fmt.Sprintf(format, args...), Err: err}
}
