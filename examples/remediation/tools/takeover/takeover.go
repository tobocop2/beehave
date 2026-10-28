// Package takeover announces an intent to take over the pull request of somebody else, then says when it may be taken.
// Three steps: a dry read, an apply that posts the notice, and a take after the grace period. The take step decides and writes no claim: the run of the item holds the claim.
package takeover

import (
	"context"
	"time"

	"github.com/tobocop2/obee/internal/model"
)

// CodeGuard is the exit code of a pull request that must not be touched. CodeRule is the exit code of a rule of the takeover.
const (
	CodeGuard = 2
	CodeRule  = 3
)

// Reader is the forge reads and the one write of the takeover.
type Reader interface {
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	ReviewComments(ctx context.Context, pr int) ([]model.Comment, error)
	PostComment(ctx context.Context, pr int, body string) error
}

// Mode is the step of the takeover.
type Mode string

// The steps. The dry step writes nothing.
const (
	ModeDry   Mode = ""
	ModeApply Mode = "apply"
	ModeTake  Mode = "take"
)

// Request is what the caller asked for. HeadWant is the head the caller works on; an empty one is not checked.
// ReplyRead says the caller read the replies that landed after the notice.
type Request struct {
	PR        int
	Mode      Mode
	ReplyRead bool
	HeadWant  string
}

// Context is the settings of the round and the calls of the takeover. The caller passes the clock as Now.
// Free refuses when another owner holds the claim and writes nothing. No call in the takeover writes a claim.
type Context struct {
	Me    string
	Idle  time.Duration
	Grace time.Duration
	Now   time.Time
	Free  func(ctx context.Context) error
}

// Result is the verdict. Code is 0, CodeGuard or CodeRule. Lines are the lines to show, a refusal last.
type Result struct {
	Code  int
	Lines []string
}
