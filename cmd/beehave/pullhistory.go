package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

// History lists the pull requests of the item in every state: those on its branch and, for an item that is a forge number, those that name it.
// Each closed or merged one gets the body of its last comment made at or before its close as its closing comment. A project with no forge has none.
func (p pulls) History(ctx context.Context, project, item string) ([]model.PriorPull, error) {
	built, err := p.forgeOf(project)
	if errors.Is(err, forge.ErrNoForge) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	rows, err := historyRows(ctx, built, item)
	if err != nil {
		return nil, err
	}

	return priorPulls(ctx, built, rows)
}

// historyRows lists the pull requests on the branch of the item and adds, by number, those whose title or body names the item as a whole run.
func historyRows(ctx context.Context, built forge.Forge, item string) ([]model.PullRequest, error) {
	rows, err := built.BranchPulls(ctx, model.ItemBranch(item))
	if err != nil {
		return nil, fmt.Errorf("list the pull requests on the branch of item %s: %w", item, err)
	}

	if model.ItemKind(item) != model.KindForge {
		return rows, nil
	}

	named, err := namingRows(ctx, built, item)
	if err != nil {
		return nil, err
	}

	rows = append(rows, named...)
	slices.SortStableFunc(rows, func(a, b model.PullRequest) int { return cmp.Compare(a.Number, b.Number) })

	return slices.CompactFunc(rows, func(a, b model.PullRequest) bool { return a.Number == b.Number }), nil
}

// namingRows lists the pull requests whose title or body names the issue item as a whole run. The search of the forge also matches a longer number, so the rows are filtered again.
func namingRows(ctx context.Context, built forge.Forge, item string) ([]model.PullRequest, error) {
	term := mention(item)

	found, err := built.SearchPulls(ctx, term+" in:title,body")
	if err != nil {
		return nil, fmt.Errorf("search the pull requests that name item %s: %w", item, err)
	}

	return slices.DeleteFunc(found, func(row model.PullRequest) bool { return !names(row.Title, term) && !names(row.Body, term) }), nil
}

// priorPulls makes the history from the listed rows, with the closing comment of each pull request that is not open.
func priorPulls(ctx context.Context, built forge.Forge, rows []model.PullRequest) ([]model.PriorPull, error) {
	prior := make([]model.PriorPull, 0, len(rows))

	for _, row := range rows {
		pull := model.PriorPull{
			Number: row.Number, Title: row.Title, State: model.Deref(row.State), Branch: model.Deref(row.HeadRefName),
			Base: model.Deref(row.BaseRefName), MergedAt: model.Deref(row.MergedAt),
			Foreign: model.Authored{Number: row.Number, Association: row.AuthorAssociation}.Foreign(),
		}

		closing, err := closingComment(ctx, built, row)
		if err != nil {
			return nil, err
		}

		pull.ClosingComment = closing.Body
		pull.ClosingForeign = closing.Body != "" && closing.AuthorAssociation.Foreign()
		prior = append(prior, pull)
	}

	return prior, nil
}

// closingComment is the last comment made at or before the close of a pull request that is not open.
// It is empty for an open one and for one with no comment until its close. A comment after the close is a later remark, so it does not count.
func closingComment(ctx context.Context, built forge.Forge, row model.PullRequest) (model.Comment, error) {
	if model.Deref(row.State) == stateOpen {
		return model.Comment{}, nil
	}

	closed, err := time.Parse(time.RFC3339, model.Deref(row.ClosedAt))
	if err != nil {
		return model.Comment{}, fmt.Errorf("pull request %d is %s but the forge gave no valid close time: %w", row.Number, model.Deref(row.State), err)
	}

	comments, err := built.Comments(ctx, row.Number)
	if err != nil {
		return model.Comment{}, fmt.Errorf("read the closing comment of pull request %d: %w", row.Number, err)
	}

	return lastCommentBy(comments, closed, row.Number)
}

// lastCommentBy is the last comment made at or before the time. The forge lists the comments oldest first, so the last match wins.
func lastCommentBy(comments []model.Comment, by time.Time, number int) (model.Comment, error) {
	var last model.Comment

	for _, comment := range comments {
		at, err := time.Parse(time.RFC3339, comment.CreatedAt)
		if err != nil {
			return model.Comment{}, fmt.Errorf("a comment of pull request %d has no valid time: %w", number, err)
		}

		if !at.After(by) {
			last = comment
		}
	}

	return last, nil
}
