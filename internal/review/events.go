package review

import (
	"context"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

const commentKind = "comment"

// event is something a person wrote on an item: a comment, an inline comment or a review.
type event struct {
	login, at, kind string
}

func isBot(login string) bool {
	return strings.Contains(login, "[bot]") || slices.Contains([]string{"github-actions", "dependabot", "codecov", "copilot"}, login)
}

// itemEvents lists everything someone wrote on an item. An issue has no review paths, so only a pull request is asked for them.
func itemEvents(ctx context.Context, f Reader, isPR bool, number int) ([]event, error) {
	comments, err := f.Comments(ctx, number)
	if err != nil {
		return nil, err
	}

	events := commentEvents(comments, nil)
	if !isPR {
		return events, nil
	}

	return reviewEvents(ctx, f, number, events)
}

// reviewEvents adds the inline comments and the sent reviews of a pull request to the events.
func reviewEvents(ctx context.Context, f Reader, number int, events []event) ([]event, error) {
	inline, err := f.ReviewComments(ctx, number)
	if err != nil {
		return nil, err
	}

	reviews, err := f.Reviews(ctx, number)
	if err != nil {
		return nil, err
	}

	events = commentEvents(inline, events)

	for _, r := range reviews {
		if r.SubmittedAt != nil {
			events = append(events, event{model.OrNull(r.User.Login), *r.SubmittedAt, string(r.State)})
		}
	}

	return events, nil
}

func commentEvents(comments []model.Comment, into []event) []event {
	for _, c := range comments {
		into = append(into, event{model.OrNull(c.User.Login), c.CreatedAt, commentKind})
	}

	return into
}

// othersAfter returns the newest time after since at which someone else, not a bot, wrote something to answer. An approval is not one.
func othersAfter(events []event, me, since string) string {
	newest := ""

	for _, e := range events {
		if isBot(e.login) || e.login == me || e.kind == string(model.ReviewApproved) || e.at <= since {
			continue
		}

		newest = max(newest, e.at)
	}

	return newest
}

// latest returns the newest time at which we wrote something.
func latest(events []event, me string) string {
	newest := ""

	for _, e := range events {
		if e.login == me {
			newest = max(newest, e.at)
		}
	}

	return newest
}
