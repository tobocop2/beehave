package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/model"
	storegithub "github.com/tobocop2/beehest/internal/store/github"
)

// queueReads reads the open work of a project for the queue library: the open items of its tracker and the open pull requests of its forge.
type queueReads struct {
	items trackerItems
	pulls pulls
}

// ItemList lists the open items of the tracker of the project that carry the label, or every open item when the label is empty.
func (q queueReads) ItemList(ctx context.Context, project, label string) ([]model.Item, error) {
	found, ok := q.items.projects[project]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	tracker, err := q.items.tracker(ctx, found.Project)
	if err != nil {
		return nil, err
	}

	items, err := tracker.ItemList(ctx, label)

	return items, closeAfter(err, tracker)
}

// errNoPullState is returned when the forge shows no state for a pull request.
var errNoPullState = errors.New("the forge shows no state for the pull request")

// PullState reads the state of the pull request with the number: OPEN, CLOSED or MERGED. A project with no forge cannot show one, which is an error.
func (q queueReads) PullState(ctx context.Context, project string, pr int) (string, error) {
	built, err := q.pulls.forgeOf(project)
	if err != nil {
		return "", err
	}

	pull, err := built.PRView(ctx, pr, "state")
	if err != nil {
		return "", fmt.Errorf("read the state of pull request %d of project %s: %w", pr, project, err)
	}

	if pull.State == nil {
		return "", fmt.Errorf("%w: #%d of project %s", errNoPullState, pr, project)
	}

	return *pull.State, nil
}

// IssueState reads the state of the issue with the number on the forge of the project: OPEN or CLOSED. Only the GitHub tracker has issue numbers for ids,
// so a project of another tracker gives found false and no read runs, as in the check after a batch (batchHost.Issues). A project with no forge cannot show
// one, and a read that fails is an error.
func (q queueReads) IssueState(ctx context.Context, project string, number int) (string, bool, error) {
	found, err := q.pulls.projectOf(project)
	if err != nil {
		return "", false, err
	}

	if found.Tracker != storegithub.Tracker {
		return "", false, nil
	}

	built, err := q.pulls.forgeOf(project)
	if err != nil {
		return "", false, err
	}

	state, err := built.IssueState(ctx, number)

	return state, err == nil, err
}

// PullClosing reads the issues that merging the pull request with the number closes on the forge of the project. A project with no forge is an error that matches forge.ErrNoForge.
func (q queueReads) PullClosing(ctx context.Context, project string, pr int) ([]model.IssueRef, error) {
	built, err := q.pulls.forgeOf(project)
	if err != nil {
		return nil, err
	}

	return closingIssues(ctx, built, pr)
}

// isHole is true when the error holds holes and nothing else: every error of a join is a pull request that lacks a field of the list.
// A hole that is joined to a real failure is not a hole, so the failure is not lost.
func isHole(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts := joined.Unwrap()
		for _, part := range parts {
			if !isHole(part) {
				return false
			}
		}

		return len(parts) > 0
	}

	var hole *model.IncompletePull

	return errors.As(err, &hole)
}

// OpenPulls lists the open pull requests of the forge of the project on the base, or every open pull request when the base is empty. A project with no forge has none.
func (q queueReads) OpenPulls(ctx context.Context, project, base string) ([]model.OpenPull, error) {
	built, err := q.pulls.forgeOf(project)
	if errors.Is(err, forge.ErrNoForge) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	pulls, err := built.OpenPulls(ctx, base)
	if isHole(err) {
		// A pull request with no head branch does not fail the queue: the list still holds it, and the queue joins no item to it.
		return pulls, nil
	}

	if err != nil {
		return nil, fmt.Errorf("list the open pull requests of project %s: %w", project, err)
	}

	return pulls, nil
}
