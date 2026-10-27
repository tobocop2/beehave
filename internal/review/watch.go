package review

import (
	"context"
	"strings"
)

// OutcomeKind says what one read of a ready pull request shows.
type OutcomeKind string

// The three outcomes of a watch read.
const (
	Merged   OutcomeKind = "merged"
	Waiting  OutcomeKind = "waiting"
	SendBack OutcomeKind = "send-back"
)

// Outcome is the result of one watch read. Reason is set for SendBack only.
type Outcome struct {
	Kind   OutcomeKind
	Reason string
}

// Watch reads a ready pull request once. A merged pull request ends the watch. A change request or a conflict sends it back to draft.
// Anything else waits. A failed read is an UnreadError: a pull request that could not be read is not one that waits.
func Watch(ctx context.Context, reads Pulls, pr int) (Outcome, error) {
	standing := Pull(ctx, reads, pr)

	if standing.Merged {
		return Outcome{Kind: Merged}, nil
	}

	if len(standing.Unread) > 0 {
		return Outcome{}, unread(nil, "cannot read %s of pull request %d", strings.Join(standing.Unread, ", "), pr)
	}

	if reasons := standing.sendBack(); len(reasons) > 0 {
		return Outcome{Kind: SendBack, Reason: strings.Join(reasons, ", ")}, nil
	}

	return Outcome{Kind: Waiting}, nil
}
