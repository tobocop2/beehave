package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store"
	"github.com/tobocop2/beehave/internal/store/beads"
)

// recordingWriter is a tracker that can be written. It counts the calls and the closes.
type recordingWriter struct {
	fakeTracker

	calls  []string
	closed int
	err    error
}

func (r *recordingWriter) Create(_ context.Context, key string, item store.NewItem) (string, bool, error) {
	r.calls = append(r.calls, "create "+key+" "+item.Title)

	return "n-1", true, r.err
}

func (r *recordingWriter) SetStatus(_ context.Context, id string, status store.Status) error {
	r.calls = append(r.calls, "status "+id+" "+string(status))

	return r.err
}

func (r *recordingWriter) Comment(_ context.Context, id, key, text string) error {
	r.calls = append(r.calls, "comment "+id+" "+key+" "+text)

	return r.err
}

func (r *recordingWriter) Assign(_ context.Context, id, assignee string) error {
	r.calls = append(r.calls, "assign "+id+" "+assignee)

	return r.err
}

func (r *recordingWriter) Close() error {
	r.closed++

	return nil
}

func writerOf(tracker store.Tracker, err error) trackerWriter {
	return trackerWriter{
		projects: map[string]config.Project{"p": {Project: model.Project{Name: "p", Tracker: beads.Tracker}}},
		tracker:  func(context.Context, model.Project) (store.Tracker, error) { return tracker, err },
	}
}

func TestTrackerWriter_CallsTheWriterOfTheProjectAndClosesItAfterEveryCall(t *testing.T) {
	t.Parallel()

	rec := &recordingWriter{}
	w := writerOf(rec, nil)

	id, created, err := w.Create(t.Context(), "p", "run:k", store.NewItem{Title: "t"})
	require.NoError(t, err)
	assert.Equal(t, "n-1", id)
	assert.True(t, created)
	require.NoError(t, w.SetStatus(t.Context(), "p", "n-1", store.StatusHold))
	require.NoError(t, w.Comment(t.Context(), "p", "n-1", "run:c", "text"))
	require.NoError(t, w.Assign(t.Context(), "p", "n-1", "alice"))

	assert.Equal(t, []string{"create run:k t", "status n-1 hold", "comment n-1 run:c text", "assign n-1 alice"}, rec.calls)
	assert.Equal(t, 4, rec.closed)
}

func TestTrackerWriter_AnErrorOfTheWriterPassesThrough(t *testing.T) {
	t.Parallel()

	rec := &recordingWriter{err: errFake}
	w := writerOf(rec, nil)

	_, _, err := w.Create(t.Context(), "p", "k", store.NewItem{Title: "t"})
	require.ErrorIs(t, err, errFake)
	require.ErrorIs(t, w.SetStatus(t.Context(), "p", "1", store.StatusOpen), errFake)
	require.ErrorIs(t, w.Comment(t.Context(), "p", "1", "k", "x"), errFake)
	require.ErrorIs(t, w.Assign(t.Context(), "p", "1", "alice"), errFake)
}

func TestTrackerWriter_ATrackerWithNoWriterIsAnErrorThatNamesTheTracker(t *testing.T) {
	t.Parallel()

	closing := &closingTracker{}
	w := writerOf(closing, nil)

	_, _, err := w.Create(t.Context(), "p", "k", store.NewItem{Title: "t"})
	require.EqualError(t, err, `project p: the tracker "beads" cannot be written. Use the "local", "beads" or "github" tracker for a workflow that creates items`)
	require.Error(t, w.SetStatus(t.Context(), "p", "1", store.StatusOpen))
	require.Error(t, w.Comment(t.Context(), "p", "1", "k", "x"))
	require.Error(t, w.Assign(t.Context(), "p", "1", "alice"))
	assert.Equal(t, 4, closing.closed)
}

func TestTrackerWriter_AnUnknownProjectAndABuilderErrorFail(t *testing.T) {
	t.Parallel()

	w := writerOf(&recordingWriter{}, nil)
	_, _, err := w.Create(t.Context(), "zzz", "k", store.NewItem{Title: "t"})
	require.ErrorIs(t, err, errUnknownProject)

	w = writerOf(nil, errFake)
	require.ErrorIs(t, w.SetStatus(t.Context(), "p", "1", store.StatusOpen), errFake)
}

func TestTrackerWriter_ALocalProjectCreatesAnItemThatTheTrackerReads(t *testing.T) {
	t.Parallel()

	table := newAdapters(nil, stateAt(t.TempDir()), nil)
	w := trackerWriter{projects: map[string]config.Project{"p": {Project: localProjectNamed("p")}}, tracker: table.tracker}

	id, created, err := w.Create(t.Context(), "p", "run:k", store.NewItem{Title: "child", Body: "b"})
	require.NoError(t, err)
	assert.True(t, created)

	again, created, err := w.Create(t.Context(), "p", "run:k", store.NewItem{Title: "child", Body: "b"})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, id, again)

	require.NoError(t, w.Comment(t.Context(), "p", id, "run:c", "hello"))
	require.NoError(t, w.SetStatus(t.Context(), "p", id, store.StatusClosed))

	items := trackerItems{projects: w.projects, tracker: table.tracker}
	fields, err := items.Fields(t.Context(), "p", id)
	require.NoError(t, err)
	assert.Equal(t, "child", fields["title"])
}
