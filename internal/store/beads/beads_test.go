package beads_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
	"github.com/tobocop2/beehave/internal/store"
	"github.com/tobocop2/beehave/internal/store/beads"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
)

func task(id, title string, priority any, fields map[string]any) map[string]any {
	row := map[string]any{"id": id, "title": title, "priority": priority, "issue_type": "task"}
	for key, value := range fields {
		row[key] = value
	}

	return row
}

// newBeads returns a store whose reads answer with empty lists until a test plans otherwise.
func newBeads(t *testing.T) (*beads.Store, *proctest.Scripted) {
	t.Helper()

	run := &proctest.Scripted{}
	for _, words := range [][]string{{"ready"}, {"ready", "--label"}, {"list", "--status=open", "--type", "epic"}, {"children"}} {
		run.Plan("[]", words...)
	}

	bd, err := beads.New(run, beadstest.Block("p"))
	require.NoError(t, err)

	return bd, run
}

func TestBeadsOpenItems_CarrySeverityHoldParentAndAssignee(t *testing.T) {
	t.Parallel()

	bd, run := newBeads(t)
	run.Plan(toJSON(t, []any{
		task("p-1", "child task", 2, nil),
		task("p-2", "blocker", 0, map[string]any{"assignee": "ann"}),
		task("p-3", "held work", 4, nil),
		task("p-4", "no priority", nil, nil),
		task("p-5", "bad priority", 5, nil),
		task("p-6", "p1", 1, nil),
		task("p-7", "p3", 3, nil),
	}), "ready")
	run.Plan(toJSON(t, []any{map[string]any{"id": "p-3"}}), "ready", "--label", "hold")
	run.Plan(toJSON(t, []any{map[string]any{"id": "p-9"}, map[string]any{"id": "p-8"}}), "list", "--status=open", "--type", "epic")
	run.Plan(toJSON(t, []any{map[string]any{"id": "p-1"}}), "children", "p-9")
	run.Plan(toJSON(t, []any{map[string]any{"id": "p-1"}, map[string]any{"id": "p-2"}}), "children", "p-8")

	items, err := bd.OpenItems(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []model.Item{
		{ID: "p-1", Severity: model.SeverityP2, Parent: "p-9", Title: "child task"},
		{ID: "p-2", Severity: model.SeverityP0, Parent: "p-8", Assignee: "ann", Title: "blocker"},
		{ID: "p-3", Severity: model.SeverityP3, Hold: true, Title: "held work"},
		{ID: "p-4", Severity: model.SeverityUnknown, Title: "no priority"},
		{ID: "p-5", Severity: model.SeverityUnknown, Title: "bad priority"},
		{ID: "p-6", Severity: model.SeverityP1, Title: "p1"},
		{ID: "p-7", Severity: model.SeverityP3, Title: "p3"},
	}, items)
}

func TestBeadsOpenItems_AnEpicAndAClaimRowAreNotWork(t *testing.T) {
	t.Parallel()

	bd, run := newBeads(t)
	run.Plan(toJSON(t, []any{
		task("p-1", "work", 2, nil),
		map[string]any{"id": "p-2", "title": "big", "priority": 1, "issue_type": "epic"},
		task("p-3", "lock:gh-5 t", 3, nil),
		task("p-4", "has lock: inside", 3, nil),
	}), "ready")

	items, err := bd.OpenItems(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"p-1", "p-4"}, []string{items[0].ID, items[1].ID})
	assert.Len(t, items, 2)
}

func TestBeadsOpenItems_AsksBeadsForTheListingsItNames(t *testing.T) {
	t.Parallel()

	bd, run := newBeads(t)
	_, err := bd.OpenItems(context.Background())
	require.NoError(t, err)
	assert.Equal(t, [][]string{
		{"ready", "--limit", "5000", "--json"},
		{"ready", "--label", "hold", "--limit", "5000", "--json"},
		{"list", "--status=open", "--type", "epic", "--limit", "5000", "--json"},
	}, run.Calls)
}

func TestBeadsReads_RefuseWhatTheyCannotTrust(t *testing.T) {
	t.Parallel()

	epics := []string{"list", "--status=open", "--type", "epic"}
	full := toJSON(t, fill(5000, map[string]any{"issue_type": "task"}))
	two := toJSON(t, []any{map[string]any{"id": "p-9"}})

	tests := []struct {
		name  string
		setup func(*proctest.Scripted)
		want  string
	}{
		{name: "ready_fails", setup: func(r *proctest.Scripted) { r.PlanFail("x", "ready") }, want: "store: cannot read beads; refusing to list work"},
		{name: "ready_not_a_list", setup: func(r *proctest.Scripted) { r.Plan(`{"id":"p"}`, "ready") }, want: "cannot read the answer of `bd ready`"},
		{name: "held_fails", setup: func(r *proctest.Scripted) { r.PlanFail("x", "ready", "--label") }, want: "cannot read the held items; refusing to list work"},
		{name: "full_ready_page", setup: func(r *proctest.Scripted) { r.Plan(full, "ready") }, want: "the ready list returned 5000 rows"},
		{name: "full_held_page", setup: func(r *proctest.Scripted) { r.Plan(full, "ready", "--label") }, want: "the held list returned 5000 rows"},
		{name: "containers_fail", setup: func(r *proctest.Scripted) { r.PlanFail("x", epics...) }, want: "cannot read beads containers; refusing"},
		{name: "children_fail", setup: func(r *proctest.Scripted) { r.Plan(two, epics...); r.PlanFail("x", "children") }, want: "cannot read the children of p-9; refusing to list work"},
		{name: "children_not_a_list", setup: func(r *proctest.Scripted) { r.Plan(two, epics...); r.Plan(`{"id":"p"}`, "children") }, want: "cannot read the answer of `bd children`"},
		{name: "child_without_id", setup: func(r *proctest.Scripted) { r.Plan(two, epics...); r.Plan(`[{"title":"x"}]`, "children") }, want: "no readable id"},
		{name: "ready_row_without_title", setup: func(r *proctest.Scripted) { r.Plan(`[{"id":"p-1"}]`, "ready") }, want: "no readable title"},
		{name: "ready_row_without_id", setup: func(r *proctest.Scripted) { r.Plan(`[{"title":"t"}]`, "ready") }, want: "no readable id"},
		{name: "row_with_text_priority", setup: func(r *proctest.Scripted) { r.Plan(`[{"id":"p-1","title":"t","priority":"2"}]`, "ready") }, want: "cannot read the answer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bd, run := newBeads(t)
			tt.setup(run)

			_, err := bd.OpenItems(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, store.CodeRefused, codeOf(t, err))
		})
	}
}

func TestBeadsOpenItems_OneRowUnderAFullPageIsListed(t *testing.T) {
	t.Parallel()

	bd, run := newBeads(t)
	run.Plan(toJSON(t, fill(4999, map[string]any{"issue_type": "task"})), "ready")

	items, err := bd.OpenItems(context.Background())
	require.NoError(t, err)
	assert.Len(t, items, 4999)
}
