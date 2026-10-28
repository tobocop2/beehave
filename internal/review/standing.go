package review

import (
	"context"
	"fmt"

	"github.com/tobocop2/obee/internal/model"
)

const (
	stateMerged = "MERGED"
	conflicting = "CONFLICTING"
)

// Pulls is the forge reads of the ready read and the ready watch. The forge backend implements it.
type Pulls interface {
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	OpenChangeRequests(ctx context.Context, pr int) ([]model.ChangeRequest, error)
	UnresolvedThreads(ctx context.Context, pr int) ([]model.ReviewThread, error)
}

// Standing is what the forge shows about one pull request: the facts that hold it back from merge.
// Unread names each read that failed. A failed read is a reason, never an empty fact.
type Standing struct {
	Number            int
	Head              string
	Merged            bool
	Conflicting       bool
	ChangeRequests    []model.ChangeRequest
	UnresolvedThreads int
	Unread            []string
}

// Pull reads the standing of a pull request. It never returns an empty fact for a read that failed: the read goes into Unread.
func Pull(ctx context.Context, reads Pulls, pr int) Standing {
	standing := Standing{Number: pr}
	standing.readView(ctx, reads)

	changes, err := reads.OpenChangeRequests(ctx, pr)
	if err != nil {
		standing.Unread = append(standing.Unread, "change requests")
	}

	standing.ChangeRequests = changes

	threads, err := reads.UnresolvedThreads(ctx, pr)
	if err != nil {
		standing.Unread = append(standing.Unread, "review threads")
	}

	standing.UnresolvedThreads = len(threads)

	return standing
}

// readView fills the head, the merge state and the conflict flag from the view of the pull request.
func (s *Standing) readView(ctx context.Context, reads Pulls) {
	view, err := reads.PRView(ctx, s.Number, "state", "mergeable", "headRefOid")
	if err != nil {
		s.Unread = append(s.Unread, "pull request")

		return
	}

	s.Head = derefOr(view.HeadRefOid, "")
	s.Merged = derefOr(view.State, "") == stateMerged
	s.Conflicting = derefOr(view.Mergeable, "") == conflicting
}

// Reasons lists why the pull request is not ready, in the order the checks run. reviewed is the commit the review read.
func (s Standing) Reasons(reviewed string) []string {
	var reasons []string

	for _, read := range s.Unread {
		reasons = append(reasons, fmt.Sprintf("the %s could not be read", read))
	}

	if s.Head != reviewed {
		reasons = append(reasons, fmt.Sprintf("it moved to %q after the review read %q", s.Head, reviewed))
	}

	reasons = append(reasons, s.sendBack()...)

	if s.UnresolvedThreads > 0 {
		reasons = append(reasons, fmt.Sprintf("%d review threads are unresolved", s.UnresolvedThreads))
	}

	return reasons
}

// sendBack lists the reasons that return a ready pull request to draft: a change request and a conflict.
func (s Standing) sendBack() []string {
	var reasons []string

	if len(s.ChangeRequests) > 0 {
		reasons = append(reasons, "a review asks for changes")
	}

	if s.Conflicting {
		reasons = append(reasons, "it conflicts with its base branch")
	}

	return reasons
}
