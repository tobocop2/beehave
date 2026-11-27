package review

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/tobocop2/beehest/internal/model"
)

// Poll returns the rows of the poll: pull requests to review, our own pull requests that need a fix, replies to answer and failed checks.
// The clock is a parameter. The search for replies looks back lookbackDays days from now.
func Poll(ctx context.Context, f Reader, own Own, me string, now time.Time, lookbackDays int) ([]Row, error) {
	since := now.UTC().AddDate(0, 0, -lookbackDays).Format(time.DateOnly)
	steps := []func() ([]Row, error){
		func() ([]Row, error) { return reviewRows(ctx, f, me, now) },
		func() ([]Row, error) { return ownRows(ctx, own, me) },
		func() ([]Row, error) { return replyRows(ctx, f, me, since) },
		func() ([]Row, error) { return checkRows(ctx, f, me) },
	}

	rows := []Row{}

	for _, step := range steps {
		got, err := step()
		if err != nil {
			return nil, err
		}

		rows = append(rows, got...)
	}

	return rows, nil
}

// numbers lists the open pull requests of a listing. A failed list stops the poll.
func numbers(ctx context.Context, f Reader, args ...string) ([]int, error) {
	rows, err := f.PRList(ctx, ListLimit, append([]string{"--state", "open"}, append(args, "--json", "number")...)...)
	if err != nil {
		return nil, unread(err, "cannot list pull requests (%s)", args)
	}

	found := make([]int, len(rows))
	for i, row := range rows {
		found[i] = row.Number
	}

	return found, nil
}

// rowsOf builds a row for each item and keeps the rows that are set. A row with no kind means the item needs nothing.
func rowsOf[T any](items []T, build func(T) (Row, error)) ([]Row, error) {
	rows := []Row{}

	for _, item := range items {
		row, err := build(item)
		if err != nil {
			return nil, err
		}

		if row.Kind != "" {
			rows = append(rows, row)
		}
	}

	return rows, nil
}

// reviewRows classifies every open pull request that others wrote and that asks for, or had, our review.
func reviewRows(ctx context.Context, f Reader, me string, now time.Time) ([]Row, error) {
	found, err := reviewable(ctx, f, me)
	if err != nil {
		return nil, err
	}

	return rowsOf(found, func(number int) (Row, error) { return classify(ctx, f, me, number, now) })
}

// reviewable lists the numbers of the three listings, sorted and without duplicates.
func reviewable(ctx context.Context, f Reader, me string) ([]int, error) {
	var found []int

	for _, search := range []string{"review-requested:" + me + " -author:" + me, "reviewed-by:" + me + " -author:" + me, "-author:" + me + " -author:app/dependabot"} {
		got, err := numbers(ctx, f, "--search", search)
		if err != nil {
			return nil, err
		}

		found = append(found, got...)
	}

	slices.Sort(found)

	return slices.Compact(found), nil
}

func classify(ctx context.Context, f Reader, me string, number int, now time.Time) (Row, error) {
	view, reviews, err := readPull(ctx, f, number)
	if err != nil {
		return Row{}, err
	}

	row, when := newRow(number, view, reviews, me)

	if row, err = withFacts(ctx, f, me, row, when, now); err != nil {
		return Row{}, err
	}

	if row.Kind == KindCurrent && requested(view, me) {
		row.Kind = KindReReview
	}

	return row, nil
}

// withFacts adds what the poll reads beyond the reviews: the deadline of a FOLLOW-UP row, the ready marks of a draft, and a reply on a row that waits for one.
func withFacts(ctx context.Context, f Reader, me string, row Row, reviewed string, now time.Time) (Row, error) {
	if row.Kind == KindFollowUp {
		if err := withDeadline(&row, reviewed, now); err != nil {
			return Row{}, err
		}
	}

	row, err := withReadyMarks(ctx, f, row)
	if err != nil || (row.Kind != KindCurrent && row.Kind != KindWaiting) {
		return row, err
	}

	return withReply(ctx, f, me, row, reviewed)
}

// withDeadline gives a FOLLOW-UP row its deadline, the time of our review plus FollowUpWindow, and whether it is due at now. A review time that is missing or not a time stops the poll:
// a deadline that cannot be computed must not read as a row that has just started.
func withDeadline(row *Row, reviewed string, now time.Time) error {
	at, err := time.Parse(time.RFC3339, reviewed)
	if err != nil {
		return unread(err, "cannot read the time of our review of #%d (%q)", row.Number, reviewed)
	}

	deadline := at.Add(FollowUpWindow)
	due := !now.Before(deadline)
	row.Deadline, row.Due = deadline.UTC().Format(time.RFC3339), &due

	return nil
}

// withReadyMarks counts the times a reviewer marked a draft ready, and lets the draft go at the second mark. Only a draft is read. A mark by the author or by an account that is gone is not a reviewer's mark.
func withReadyMarks(ctx context.Context, f Reader, row Row) (Row, error) {
	if row.IsDraft == nil || !*row.IsDraft {
		return row, nil
	}

	marks, err := f.ReadyMarks(ctx, row.Number)
	if err != nil {
		return Row{}, unread(err, "cannot read the ready marks of #%d. Check the access to the repository, then run again", row.Number)
	}

	for _, login := range marks {
		if login != "" && !strings.EqualFold(login, row.Author) {
			row.ReadyMarks++
		}
	}

	row.LetGo = row.ReadyMarks >= LetGoMarks

	return row, nil
}

// readPull reads a pull request and its reviews.
func readPull(ctx context.Context, f Reader, number int) (model.PullRequest, []model.Review, error) {
	view, err := f.PRView(ctx, number, "number", "title", "author", "headRefOid", "isDraft", "updatedAt", "reviewRequests")
	if err != nil {
		return view, nil, unread(err, "cannot read #%d", number)
	}

	reviews, err := f.Reviews(ctx, number)
	if err != nil {
		return view, nil, unread(err, "cannot read the reviews of #%d", number)
	}

	return view, reviews, nil
}

// newRow returns the row of a pull request from our last verdict, and the time of that verdict.
func newRow(number int, view model.PullRequest, reviews []model.Review, me string) (Row, string) {
	head := derefOr(view.HeadRefOid, "null")
	state, at, when := myState(reviews, me)

	row := Row{
		Kind: kindOf(state, at, head), Number: number, Author: model.OrNull(view.Author.Login), Head: head, Ours: state, OursAt: at,
		IsDraft: view.IsDraft, UpdatedAt: view.UpdatedAt, Title: view.Title,
	}

	if state != noReview {
		still := at == head
		row.OursWhen, row.StillHead = when, &still
	}

	return row, when
}

// withReply makes a row REPLIED when someone else wrote after our verdict.
func withReply(ctx context.Context, f Reader, me string, row Row, when string) (Row, error) {
	events, err := itemEvents(ctx, f, true, row.Number)
	if err != nil {
		return Row{}, unread(err, "cannot read the comments of #%d", row.Number)
	}

	if row.ReplyAt = othersAfter(events, me, when); row.ReplyAt != "" {
		row.Kind = KindReplied
	}

	return row, nil
}

func derefOr(text *string, fallback string) string {
	if text == nil {
		return fallback
	}

	return *text
}

func kindOf(state, at, head string) Kind {
	switch {
	case state == noReview:
		return KindNew
	case at == head && state == string(model.ReviewChangesRequested):
		return KindWaiting
	case at == head:
		return KindCurrent
	case state == string(model.ReviewChangesRequested):
		return KindFollowUp
	}

	return KindReReview
}

// myState returns the state, commit and time of our last review that is a verdict. A comment, a dismissed review and a draft review are not one.
func myState(reviews []model.Review, me string) (state, commit, when string) {
	for _, r := range slices.Backward(reviews) {
		if r.User.Login != me || r.State == model.ReviewCommented || r.State == model.ReviewDismissed || r.State == model.ReviewPending {
			continue
		}

		return orDefault(string(r.State), noReview), orDefault(r.CommitID, none), orDefault(derefOr(r.SubmittedAt, ""), none)
	}

	return noReview, none, none
}

func orDefault(text, fallback string) string {
	if text == "" {
		return fallback
	}

	return text
}

func requested(view model.PullRequest, me string) bool {
	for _, ask := range view.ReviewRequests {
		if model.OrNull(orDefault(ask.Login, ask.Name)) == me {
			return true
		}
	}

	return false
}
