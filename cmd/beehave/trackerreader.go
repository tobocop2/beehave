package main

import (
	"context"
	"fmt"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/store"
)

// trackerReader reads the comments and the parents of the items of the tracker of a project. It opens the tracker for each call and closes it after, as trackerWriter does.
type trackerReader struct {
	projects map[string]config.Project
	tracker  trackerBuilder
}

// open builds the tracker of the project and returns its reader. A tracker with no reader is an error that names it; the tracker is closed first.
func (t trackerReader) open(ctx context.Context, project string) (store.Reader, error) { //nolint:ireturn // the tracker table builds whichever adapter the project names
	found, ok := t.projects[project]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	tracker, err := t.tracker(ctx, found.Project)
	if err != nil {
		return nil, err
	}

	reader, ok := tracker.(store.Reader)
	if !ok {
		return nil, closeAfter(fmt.Errorf("project %s: the tracker %q cannot read comments or parents. Use the \"local\", \"beads\" or \"github\" tracker for a workflow that calls item_read", project, found.Tracker), tracker)
	}

	return reader, nil
}

// Comments returns the comments of an item of the tracker of the project.
func (t trackerReader) Comments(ctx context.Context, project, id string) ([]string, error) {
	reader, err := t.open(ctx, project)
	if err != nil {
		return nil, err
	}

	comments, err := reader.Comments(ctx, id)

	return comments, closeAfter(err, reader)
}

// Parent returns the parent of an item of the tracker of the project, or an empty text when the item has none.
func (t trackerReader) Parent(ctx context.Context, project, id string) (string, error) {
	reader, err := t.open(ctx, project)
	if err != nil {
		return "", err
	}

	parent, err := reader.Parent(ctx, id)

	return parent, closeAfter(err, reader)
}
