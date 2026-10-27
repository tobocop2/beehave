package review

import (
	"context"
	"strconv"

	"github.com/tobocop2/obee/internal/model"
)

// ownRows lists our open pull requests with a review after the last push, an open thread or a standing change request.
func ownRows(ctx context.Context, own Own, me string) ([]Row, error) {
	pulls, err := own.OwnPulls(ctx, me)
	if err != nil {
		return nil, unread(err, "cannot read our open pull requests")
	}

	return rowsOf(pulls, func(pull model.OwnPull) (Row, error) { return ownRow(pull, me) })
}

// ownRow returns the OWN row of a pull request, or an empty row when nothing waits on us.
func ownRow(pull model.OwnPull, me string) (Row, error) {
	if !pull.ChangesKnown {
		return Row{}, unread(nil, "cannot read the reviews of #%d", pull.Number)
	}

	fresh, open := freshReviews(pull, me), unresolved(pull)
	if fresh == 0 && open == "0" && len(pull.ChangeRequests) == 0 {
		return Row{}, nil
	}

	draft := pull.Draft

	return Row{
		Kind: KindOwn, Number: pull.Number, FreshReviews: fresh, Unresolved: open, IsDraft: &draft,
		ChangesRequested: orDefault(model.JoinChangeRequests(pull.ChangeRequests), "none"),
	}, nil
}

// freshReviews counts the reviews of others sent after the last push.
func freshReviews(pull model.OwnPull, me string) int {
	fresh := 0

	for _, r := range pull.Reviews {
		if r.Login != me && r.SubmittedAt > pull.Push {
			fresh++
		}
	}

	return fresh
}

// unresolved is the number of open threads, or unknown when there are more threads than one page.
func unresolved(pull model.OwnPull) string {
	if !pull.ThreadsKnown {
		return "unknown"
	}

	return strconv.Itoa(pull.OpenThreads)
}
