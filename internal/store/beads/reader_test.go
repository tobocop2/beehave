package beads_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/proc/proctest"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/beads"
)

var _ store.Reader = (*beads.Store)(nil)

func readerOver(t *testing.T, run *proctest.Scripted) *beads.Store {
	t.Helper()

	bd, err := beads.New(run, "p")
	require.NoError(t, err)

	return bd
}

func TestComments_ReadsTheTextAndDropsTheMarkerLine(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(`[{"id":1,"text":"first"},{"id":2,"text":"second\n\nobee-key: run-1:note"}]`, "comments", "p-1", "--json")

	got, err := readerOver(t, run).Comments(t.Context(), "p-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "second"}, got)
	assert.Equal(t, [][]string{{"comments", "p-1", "--json"}}, run.Calls)
}

func TestComments_RefusesAFailedReadAndUnreadableText(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.PlanFail("boom", "comments")

	_, err := readerOver(t, run).Comments(t.Context(), "p-1")
	require.ErrorContains(t, err, "cannot read the comments of item p-1")

	run = &proctest.Scripted{}
	run.Plan("not json", "comments")

	_, err = readerOver(t, run).Comments(t.Context(), "p-1")
	require.Error(t, err)
}

func TestParent_ReadsTheParentFieldOfBdShow(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(`[{"id":"p-1.1","title":"child","parent":"p-1"}]`, "show", "p-1.1", "--json")

	got, err := readerOver(t, run).Parent(t.Context(), "p-1.1")
	require.NoError(t, err)
	assert.Equal(t, "p-1", got)
}

func TestParent_AnItemWithNoParentGivesNothingAndAFailedReadIsARefusal(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(`[{"id":"p-1","title":"epic"}]`, "show", "p-1", "--json")

	got, err := readerOver(t, run).Parent(t.Context(), "p-1")
	require.NoError(t, err)
	assert.Empty(t, got)

	run = &proctest.Scripted{}
	run.PlanFail("boom", "show")

	_, err = readerOver(t, run).Parent(t.Context(), "p-1")
	require.ErrorContains(t, err, "cannot read item p-1")
}
