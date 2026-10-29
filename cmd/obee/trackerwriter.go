package main

import (
	"context"
	"fmt"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/store"
)

// trackerWriter changes the items of the tracker of a project. It opens the tracker for each call and closes it after, as trackerItems does.
type trackerWriter struct {
	projects map[string]config.Project
	tracker  trackerBuilder
}

// open builds the tracker of the project and returns its writer. A tracker with no writer is an error that names it; the tracker is closed first.
func (t trackerWriter) open(ctx context.Context, project string) (store.Writer, error) { //nolint:ireturn // the tracker table builds whichever adapter the project names
	found, ok := t.projects[project]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	tracker, err := t.tracker(ctx, found.Project)
	if err != nil {
		return nil, err
	}

	writer, ok := tracker.(store.Writer)
	if !ok {
		return nil, closeAfter(fmt.Errorf("project %s: the tracker %q cannot be written. Use the \"local\", \"beads\" or \"github\" tracker for a workflow that creates items", project, found.Tracker), tracker)
	}

	return writer, nil
}

// Create files an item in the tracker of the project. The key makes a repeated call return the first item.
func (t trackerWriter) Create(ctx context.Context, project, key string, item store.NewItem) (string, bool, error) {
	writer, err := t.open(ctx, project)
	if err != nil {
		return "", false, err
	}

	id, created, err := writer.Create(ctx, key, item)

	return id, created, closeAfter(err, writer)
}

// SetStatus sets the status of an item in the tracker of the project.
func (t trackerWriter) SetStatus(ctx context.Context, project, id string, status store.Status) error {
	writer, err := t.open(ctx, project)
	if err != nil {
		return err
	}

	return closeAfter(writer.SetStatus(ctx, id, status), writer)
}

// Comment adds a comment to an item in the tracker of the project. The key makes a repeated call add one comment.
func (t trackerWriter) Comment(ctx context.Context, project, id, key, text string) error {
	writer, err := t.open(ctx, project)
	if err != nil {
		return err
	}

	return closeAfter(writer.Comment(ctx, id, key, text), writer)
}
