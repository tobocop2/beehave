package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
)

// openPullLimit is the limit of the search for the open pull requests of a project. A list at the limit is refused, so a queue never reads a cut list.
const openPullLimit = 500

// queueReads reads the open work of a project for the queue builtin: the open items of its tracker and the open pull requests of its forge.
type queueReads struct {
	items trackerItems
	pulls pulls
}

// OpenItems lists the open items of the tracker of the project.
func (q queueReads) OpenItems(ctx context.Context, project string) ([]model.Item, error) {
	found, ok := q.items.projects[project]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	tracker, err := q.items.tracker(ctx, found.Project)
	if err != nil {
		return nil, err
	}

	items, err := tracker.OpenItems(ctx)

	return items, closeAfter(err, tracker)
}

// OpenPulls lists the open pull requests of the forge of the project, with their head branch and labels. A project with no forge has none.
func (q queueReads) OpenPulls(ctx context.Context, project string) ([]model.PullRequest, error) {
	built, err := q.pulls.forgeOf(project)
	if errors.Is(err, forge.ErrNoForge) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	rows, err := built.PRList(ctx, openPullLimit, "--state", "open", "--json", "number,headRefName,labels")
	if err != nil {
		return nil, fmt.Errorf("list the open pull requests of project %s: %w", project, err)
	}

	return rows, nil
}
