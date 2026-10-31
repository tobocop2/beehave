package review

import (
	"context"
	"strings"
)

// OutcomeKind says what one read of a ready pull request shows.
type OutcomeKind string

// The outcomes of a watch read.
const (
	Merged   OutcomeKind = "merged"
	Waiting  OutcomeKind = "waiting"
	SendBack OutcomeKind = "send-back"
	// Closed is a pull request closed with no merge. It cannot return to draft, so the watch ends with Reason set.
	Closed OutcomeKind = "closed"
)

// Outcome is the result of one watch read. Reason is set for SendBack and Closed.
// Draft is true when a SendBack read finds the pull request already in draft: the draft call has nothing left to do.
type Outcome struct {
	Kind   OutcomeKind
	Reason string
	Draft  bool
}

// Watch reads a ready pull request once. A merged pull request ends the watch. A change request or a conflict sends it back to draft.
// Anything else waits. A failed read is an UnreadError: a pull request that could not be read is not one that waits.
func Watch(ctx context.Context, reads Pulls, pr int) (Outcome, error) {
	standing := Pull(ctx, reads, pr)

	if standing.Merged {
		return Outcome{Kind: Merged}, nil
	}

	if standing.Closed {
		return Outcome{Kind: Closed, Reason: "it is closed"}, nil
	}

	if len(standing.Unread) > 0 {
		return Outcome{}, unread(nil, "cannot read %s of pull request %d", strings.Join(standing.Unread, ", "), pr)
	}

	if reasons := standing.sendBack(); len(reasons) > 0 {
		return Outcome{Kind: SendBack, Reason: strings.Join(reasons, ", "), Draft: standing.Draft}, nil
	}

	return Outcome{Kind: Waiting}, nil
}
