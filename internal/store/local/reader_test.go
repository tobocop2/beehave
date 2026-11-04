package local_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/local"
)

var _ store.Reader = (*local.Store)(nil)

func TestComments_ReturnsTheCommentsOfTheItemInTheOrderTheyWereAdded(t *testing.T) {
	t.Parallel()

	s, _ := newWriter(t)
	id, _, err := s.Create(t.Context(), "run:a", store.NewItem{Title: "plan"})
	require.NoError(t, err)

	other, _, err := s.Create(t.Context(), "run:b", store.NewItem{Title: "other"})
	require.NoError(t, err)

	require.NoError(t, s.Comment(t.Context(), id, "z-first", "one"))
	require.NoError(t, s.Comment(t.Context(), id, "a-second", "two"))
	require.NoError(t, s.Comment(t.Context(), other, "k", "not mine"))
	require.NoError(t, s.Comment(t.Context(), id, "z-first", "one again"))

	got, err := s.Comments(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two"}, got)
}

func TestComments_AnItemWithNoCommentsGivesAnEmptyList(t *testing.T) {
	t.Parallel()

	s, _ := newWriter(t)
	id, _, err := s.Create(t.Context(), "run:a", store.NewItem{Title: "plan"})
	require.NoError(t, err)

	got, err := s.Comments(t.Context(), id)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestParent_ReturnsTheParentOfTheItemOrNothing(t *testing.T) {
	t.Parallel()

	s, _ := newWriter(t)
	epic, _, err := s.Create(t.Context(), "run:epic", store.NewItem{Title: "epic"})
	require.NoError(t, err)

	child, _, err := s.Create(t.Context(), "run:child", store.NewItem{Title: "child", Parent: epic})
	require.NoError(t, err)

	got, err := s.Parent(t.Context(), child)
	require.NoError(t, err)
	assert.Equal(t, epic, got)

	got, err = s.Parent(t.Context(), epic)
	require.NoError(t, err)
	assert.Empty(t, got, "an item with no parent has an empty parent")
}

func TestReader_AnItemTheProjectDoesNotHaveIsARefusal(t *testing.T) {
	t.Parallel()

	s, _ := newWriter(t)

	_, err := s.Comments(t.Context(), "p-99")
	require.ErrorContains(t, err, "no item p-99")

	_, err = s.Parent(t.Context(), "p-99")
	require.ErrorContains(t, err, "no item p-99")
}

func TestReader_AClosedFileIsAnError(t *testing.T) {
	t.Parallel()

	s, _ := newWriter(t)
	require.NoError(t, s.Close())

	_, err := s.Comments(t.Context(), "p-1")
	require.Error(t, err)

	_, err = s.Parent(t.Context(), "p-1")
	require.Error(t, err)
}
