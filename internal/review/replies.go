package review

import (
	"context"
	"maps"
	"slices"

	"github.com/tobocop2/beehave/internal/model"
)

// replyRows lists the items of the last days, issues and pull requests, where someone else wrote after our last word.
func replyRows(ctx context.Context, f Reader, me, since string) ([]Row, error) {
	items, err := involved(ctx, f, me, since)
	if err != nil {
		return nil, err
	}

	return rowsOf(slices.Sorted(maps.Keys(items)), func(number int) (Row, error) {
		return replyRow(ctx, f, me, number, items[number])
	})
}

// involved maps the number of each item we took part in to whether it is a pull request.
func involved(ctx context.Context, f Reader, me, since string) (map[int]bool, error) {
	items := map[int]bool{}

	for _, kind := range []model.SearchKind{model.SearchIssues, model.SearchPulls} {
		found, err := f.Search(ctx, model.SearchRequest{Kind: kind, Involves: me, Since: since, Limit: SearchLimit})
		if err != nil {
			return nil, unread(err, "cannot search %s", kind)
		}

		for _, number := range found {
			items[number] = items[number] || kind == model.SearchPulls
		}
	}

	return items, nil
}

// replyRow returns a REPLY row, or a row with no ReplyAt when we had the last word. A title that cannot be read shows as empty.
func replyRow(ctx context.Context, f Reader, me string, number int, isPR bool) (Row, error) {
	events, err := itemEvents(ctx, f, isPR, number)
	if err != nil {
		return Row{}, unread(err, "cannot read the comments of #%d", number)
	}

	theirs := othersAfter(events, me, orDefault(latest(events, me), "0"))
	if theirs == "" {
		return Row{}, nil
	}

	title, err := f.Title(ctx, number)
	if err != nil {
		title = ""
	}

	return Row{Kind: KindReply, Number: number, ReplyAt: theirs, Title: title}, nil
}

// checkRows lists our ready pull requests with a failed check, split into checks that ran and checks that never started.
func checkRows(ctx context.Context, f Reader, me string) ([]Row, error) {
	ours, err := f.PRList(ctx, ListLimit, "--state", "open", "--author", me, "--json", "number,isDraft")
	if err != nil {
		return nil, unread(err, "cannot list our ready pull requests")
	}

	return rowsOf(ours, func(pull model.PullRequest) (Row, error) {
		if pull.IsDraft != nil && *pull.IsDraft {
			return Row{}, nil
		}

		return checkRow(ctx, f, pull.Number)
	})
}

func checkRow(ctx context.Context, f Reader, number int) (Row, error) {
	checks, err := f.PRChecks(ctx, number)
	if err != nil {
		return Row{}, unread(err, "cannot read the checks of #%d", number)
	}

	failed, notStarted := countFailed(ctx, f, checks)

	switch {
	case failed > 0:
		return Row{Kind: KindCIRed, Number: number, Failed: failed, NotStarted: notStarted}, nil
	case notStarted > 0:
		return Row{Kind: KindCINotRun, Number: number, NotStarted: notStarted}, nil
	}

	return Row{}, nil
}

// countFailed splits the failed checks that have a link into checks that ran and checks that never started.
func countFailed(ctx context.Context, f Reader, checks []model.Check) (failed, notStarted int) {
	for _, c := range checks {
		if c.Bucket != "fail" || c.Link == "" {
			continue
		}

		// A failed annotation read is no proof that the check never started, so the check counts as one that ran.
		if never, err := f.CheckNotStarted(ctx, c.Link); err == nil && never {
			notStarted++
		} else {
			failed++
		}
	}

	return failed, notStarted
}
