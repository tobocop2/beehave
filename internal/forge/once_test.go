package forge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

var errRead = errors.New("read failed")

// notes is an Activity that keeps the comments that PostComment adds. Only the three methods of PostOnce are live.
type notes struct {
	forge.Activity

	rows    []model.Comment
	posted  []string
	readErr error
}

func (n *notes) Comments(context.Context, int) ([]model.Comment, error) { return n.rows, n.readErr }

func (n *notes) PostComment(_ context.Context, _ int, body string) error {
	n.posted = append(n.posted, body)
	n.rows = append(n.rows, model.Comment{Body: body})

	return nil
}

func TestPostOnce_TheSameKeyPostsOneComment(t *testing.T) {
	t.Parallel()

	n := &notes{}

	require.NoError(t, forge.PostOnce(t.Context(), n, 7, "run:note", "one line"))
	require.NoError(t, forge.PostOnce(t.Context(), n, 7, "run:note", "one line"))
	assert.Equal(t, []string{"one line\n\nbeehave-key: run:note"}, n.posted)

	require.NoError(t, forge.PostOnce(t.Context(), n, 7, "run:note2", "other"))
	assert.Len(t, n.posted, 2)
}

func TestPostOnce_AKeyThatIsAPrefixIsAnotherKey(t *testing.T) {
	t.Parallel()

	n := &notes{rows: []model.Comment{{Body: "text\r\n\r\nbeehave-key: run:10"}}}

	require.NoError(t, forge.PostOnce(t.Context(), n, 7, "run:1", "text"))
	assert.Len(t, n.posted, 1)

	require.NoError(t, forge.PostOnce(t.Context(), n, 7, "run:10", "text"))
	assert.Len(t, n.posted, 1)
}

func TestPostOnce_AFailedReadPostsNothing(t *testing.T) {
	t.Parallel()

	n := &notes{readErr: errRead}

	err := forge.PostOnce(t.Context(), n, 7, "k", "text")
	require.ErrorIs(t, err, errRead)
	assert.Empty(t, n.posted)
}
