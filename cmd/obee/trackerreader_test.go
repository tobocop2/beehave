package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
)

// recordingReader is a tracker that can be read beyond its item fields. It counts the calls and the closes.
type recordingReader struct {
	fakeTracker

	calls  []string
	closed int
	err    error
}

func (r *recordingReader) Comments(_ context.Context, id string) ([]string, error) {
	r.calls = append(r.calls, "comments "+id)

	return []string{"note"}, r.err
}

func (r *recordingReader) Parent(_ context.Context, id string) (string, error) {
	r.calls = append(r.calls, "parent "+id)

	return "p-1", r.err
}

func (r *recordingReader) Close() error {
	r.closed++

	return nil
}

func readerOf(tracker store.Tracker, err error) trackerReader {
	return trackerReader{
		projects: map[string]config.Project{"p": {Project: model.Project{Name: "p", Tracker: model.TrackerBeads}}},
		tracker:  func(context.Context, model.Project) (store.Tracker, error) { return tracker, err },
	}
}

func TestTrackerReader_ReadsThroughTheReaderOfTheProjectAndClosesItAfterEveryCall(t *testing.T) {
	t.Parallel()

	rec := &recordingReader{}
	r := readerOf(rec, nil)

	comments, err := r.Comments(t.Context(), "p", "n-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"note"}, comments)

	parent, err := r.Parent(t.Context(), "p", "n-1")
	require.NoError(t, err)
	assert.Equal(t, "p-1", parent)

	assert.Equal(t, []string{"comments n-1", "parent n-1"}, rec.calls)
	assert.Equal(t, 2, rec.closed)
}

func TestTrackerReader_ATrackerWithNoReaderRefusesAndNamesTheFix(t *testing.T) {
	t.Parallel()

	r := readerOf(&fakeTracker{}, nil)

	_, err := r.Comments(t.Context(), "p", "n-1")
	require.ErrorContains(t, err, "cannot read comments or parents")
	require.ErrorContains(t, err, `"beads"`)

	_, err = r.Parent(t.Context(), "p", "n-1")
	require.ErrorContains(t, err, "cannot read comments or parents")
}

func TestTrackerReader_AnUnknownProjectAndABuildFailureAreErrors(t *testing.T) {
	t.Parallel()

	_, err := readerOf(&recordingReader{}, nil).Comments(t.Context(), "other", "n-1")
	require.ErrorIs(t, err, errUnknownProject)

	boom := errors.New("boom")

	_, err = readerOf(nil, boom).Parent(t.Context(), "p", "n-1")
	require.ErrorIs(t, err, boom)
}

func TestTrackerReader_AnErrorOfTheReaderPassesThrough(t *testing.T) {
	t.Parallel()

	rec := &recordingReader{err: errors.New("read failed")}

	_, err := readerOf(rec, nil).Comments(t.Context(), "p", "n-1")
	require.ErrorContains(t, err, "read failed")
	assert.Equal(t, 1, rec.closed)
}
