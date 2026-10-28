package beads_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/proc/proctest"
)

func TestBeadsItemFields_DeclareIDTitleAndBody(t *testing.T) {
	t.Parallel()

	bd, _ := newBeads(t)

	assert.Equal(t, []string{"id", "title", "body"}, bd.ItemFields())
}

func TestBeadsItem_ReadsTheDeclaredFields(t *testing.T) {
	t.Parallel()

	bd, run := newBeads(t)
	run.Plan(`[{"id":"p-1","title":"T","description":"Body"}]`, "show")

	got, err := bd.Item(context.Background(), "p-1")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"id": "p-1", "title": "T", "body": "Body"}, got)
	assert.Equal(t, []string{"show", "p-1", "--json"}, run.Calls[0])
}

func TestBeadsItem_AnItemWithNoDescriptionHasAnEmptyBody(t *testing.T) {
	t.Parallel()

	bd, run := newBeads(t)
	run.Plan(`{"id":"p-1","title":"T"}`, "show")

	got, err := bd.Item(context.Background(), "p-1")
	require.NoError(t, err)
	assert.Empty(t, got["body"])
}

func TestBeadsItem_Refuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*proctest.Scripted)
		want  string
	}{
		{name: "show_fails", setup: func(r *proctest.Scripted) { r.PlanFail("x", "show") }, want: "REFUSE: cannot read item p-1 in beads"},
		{name: "empty_list", setup: func(r *proctest.Scripted) { r.Plan("[]", "show") }, want: "beads returned nothing for p-1"},
		{name: "list_not_rows", setup: func(r *proctest.Scripted) { r.Plan("[1]", "show") }, want: "cannot read the answer of `bd show`"},
		{name: "empty_answer", setup: func(r *proctest.Scripted) { r.Plan("", "show") }, want: "cannot read the answer of `bd show`"},
		{name: "no_title", setup: func(r *proctest.Scripted) { r.Plan(`{"id":"p-1"}`, "show") }, want: "no readable title"},
		{name: "no_id", setup: func(r *proctest.Scripted) { r.Plan(`{"title":"T"}`, "show") }, want: "no readable id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bd, run := newBeads(t)
			tt.setup(run)

			_, err := bd.Item(context.Background(), "p-1")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}
