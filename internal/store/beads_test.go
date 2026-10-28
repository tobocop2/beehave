package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
)

func task(id, title string, priority any, fields map[string]any) map[string]any {
	row := map[string]any{"id": id, "title": title, "priority": priority, "issue_type": "task"}
	for key, value := range fields {
		row[key] = value
	}

	return row
}

// newBeads returns a store whose reads answer with empty lists until a test plans otherwise.
func newBeads(t *testing.T) (*store.BeadsStore, *scripted) {
	t.Helper()

	run := &scripted{}
	for _, words := range [][]string{{"ready"}, {"ready", "--label"}, {"list", "--status=open", "--type", "epic"}, {"children"}} {
		run.plan("[]", words...)
	}

	bd, err := store.NewBeadsStore(run, "p")
	require.NoError(t, err)

	return bd, run
}

func TestBeadsOpenItems_CarrySeverityHoldParentAndAssignee(t *testing.T) {
	t.Parallel()

	bd, run := newBeads(t)
	run.plan(toJSON(t, []any{
		task("p-1", "child task", 2, nil),
		task("p-2", "blocker", 0, map[string]any{"assignee": "ann"}),
		task("p-3", "held work", 4, nil),
		task("p-4", "no priority", nil, nil),
		task("p-5", "bad priority", 5, nil),
		task("p-6", "p1", 1, nil),
		task("p-7", "p3", 3, nil),
	}), "ready")
	run.plan(toJSON(t, []any{map[string]any{"id": "p-3"}}), "ready", "--label", "hold")
	run.plan(toJSON(t, []any{map[string]any{"id": "p-9"}, map[string]any{"id": "p-8"}}), "list", "--status=open", "--type", "epic")
	run.plan(toJSON(t, []any{map[string]any{"id": "p-1"}}), "children", "p-9")
	run.plan(toJSON(t, []any{map[string]any{"id": "p-1"}, map[string]any{"id": "p-2"}}), "children", "p-8")

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
	run.plan(toJSON(t, []any{
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
	}, run.calls)
}

func TestBeadsReads_RefuseWhatTheyCannotTrust(t *testing.T) {
	t.Parallel()

	epics := []string{"list", "--status=open", "--type", "epic"}
	full := toJSON(t, fill(5000, map[string]any{"issue_type": "task"}))
	two := toJSON(t, []any{map[string]any{"id": "p-9"}})

	tests := []struct {
		name  string
		setup func(*scripted)
		want  string
	}{
		{name: "ready_fails", setup: func(r *scripted) { r.planFail("x", "ready") }, want: "store: cannot read beads; refusing to list work"},
		{name: "ready_not_a_list", setup: func(r *scripted) { r.plan(`{"id":"p"}`, "ready") }, want: "cannot read the answer of `bd ready`"},
		{name: "held_fails", setup: func(r *scripted) { r.planFail("x", "ready", "--label") }, want: "cannot read the held items; refusing to list work"},
		{name: "full_ready_page", setup: func(r *scripted) { r.plan(full, "ready") }, want: "the ready list returned 5000 rows"},
		{name: "full_held_page", setup: func(r *scripted) { r.plan(full, "ready", "--label") }, want: "the held list returned 5000 rows"},
		{name: "containers_fail", setup: func(r *scripted) { r.planFail("x", epics...) }, want: "cannot read beads containers; refusing"},
		{name: "children_fail", setup: func(r *scripted) { r.plan(two, epics...); r.planFail("x", "children") }, want: "cannot read the children of p-9; refusing to list work"},
		{name: "children_not_a_list", setup: func(r *scripted) { r.plan(two, epics...); r.plan(`{"id":"p"}`, "children") }, want: "cannot read the answer of `bd children`"},
		{name: "child_without_id", setup: func(r *scripted) { r.plan(two, epics...); r.plan(`[{"title":"x"}]`, "children") }, want: "no readable id"},
		{name: "ready_row_without_title", setup: func(r *scripted) { r.plan(`[{"id":"p-1"}]`, "ready") }, want: "no readable title"},
		{name: "ready_row_without_id", setup: func(r *scripted) { r.plan(`[{"title":"t"}]`, "ready") }, want: "no readable id"},
		{name: "row_with_text_priority", setup: func(r *scripted) { r.plan(`[{"id":"p-1","title":"t","priority":"2"}]`, "ready") }, want: "cannot read the answer"},
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

func TestBeadsContainersAndChildren_AloneRefuseTheSameWay(t *testing.T) {
	t.Parallel()

	bd, run := newBeads(t)
	run.plan(`[{"id":"p-9"},{"id":"p-8"}]`, "list", "--status=open", "--type", "epic")
	run.plan(`[{"id":"p-1"}]`, "children", "p-9")

	containers, err := bd.Containers(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"p-9", "p-8"}, containers)

	children, err := bd.Children(context.Background(), "p-9")
	require.NoError(t, err)
	assert.Equal(t, []string{"p-1"}, children)

	run.planFail("x", "children")

	_, err = bd.Children(context.Background(), "p-9")
	require.EqualError(t, err, "store: cannot read the children of p-9; refusing")

	run.planFail("x", "list", "--status=open", "--type", "epic")

	_, err = bd.Containers(context.Background())
	assert.EqualError(t, err, "store: cannot read beads containers; refusing")
}

func TestBeadsOpenItems_OneRowUnderAFullPageIsListed(t *testing.T) {
	t.Parallel()

	bd, run := newBeads(t)
	run.plan(toJSON(t, fill(4999, map[string]any{"issue_type": "task"})), "ready")

	items, err := bd.OpenItems(context.Background())
	require.NoError(t, err)
	assert.Len(t, items, 4999)
}

func TestBeadsShow_PrintsTheBodyThenTheComments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		shown  string
		stdout string
		want   string
	}{
		{name: "list_of_one", shown: `[{"id":"p-1","title":"T","description":"Body"}]`, stdout: "c1\nc2\n", want: "# p-1 T\n\nBody\nc1\nc2\n"},
		{name: "object", shown: `{"id":"p-1","title":"T","description":"Body"}`, stdout: "", want: "# p-1 T\n\nBody\n"},
		{name: "no_description", shown: `{"id":"p-1","title":"T"}`, stdout: "c\n", want: "# p-1 T\n\n\nc\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bd, run := newBeads(t)
			run.plan(tt.shown, "show")
			run.plan(tt.stdout, "comments")

			got, err := bd.Show(context.Background(), "p-1")
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, []string{"show", "p-1", "--json"}, run.calls[0])
			assert.Equal(t, []string{"comments", "p-1"}, run.calls[1])
		})
	}
}

func TestBeadsShow_Refuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*scripted)
		want  string
	}{
		{name: "show_fails", setup: func(r *scripted) { r.planFail("x", "show") }, want: "REFUSE: cannot read item p-1 in beads"},
		{name: "empty_list", setup: func(r *scripted) { r.plan("[]", "show") }, want: "beads returned nothing for p-1"},
		{name: "empty_answer", setup: func(r *scripted) { r.plan("", "show") }, want: "cannot read the answer of `bd show`"},
		{name: "null_answer", setup: func(r *scripted) { r.plan("null", "show") }, want: "cannot read the answer of `bd show`"},
		{name: "list_not_rows", setup: func(r *scripted) { r.plan("[1]", "show") }, want: "cannot read the answer of `bd show`"},
		{name: "no_title", setup: func(r *scripted) { r.plan(`{"id":"p-1"}`, "show") }, want: "no readable title"},
		{name: "no_id", setup: func(r *scripted) { r.plan(`{"title":"T"}`, "show") }, want: "no readable id"},
		{name: "comments_fail", setup: func(r *scripted) { r.plan(`{"id":"p-1","title":"T"}`, "show"); r.planFail("x", "comments") }, want: "cannot read the comments of p-1 in beads"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bd, run := newBeads(t)
			tt.setup(run)

			_, err := bd.Show(context.Background(), "p-1")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, store.CodeRefused, codeOf(t, err))
		})
	}
}

func TestBeadsClose_RecordsTheReasonAndAFailedCloseCarriesCodeOne(t *testing.T) {
	t.Parallel()

	bd, run := newBeads(t)
	run.plan("", "close")
	require.NoError(t, bd.Close(context.Background(), "p-1", "done"))
	assert.Equal(t, []string{"close", "p-1", "--reason", "done"}, run.calls[0])

	failing, bad := newBeads(t)
	bad.planFail(" locked \n", "close")

	err := failing.Close(context.Background(), "p-1", "done")
	require.EqualError(t, err, "store: cannot close p-1: locked")
	assert.Equal(t, store.CodeCloseFailed, codeOf(t, err))
}

func TestBeadsCreate_FilesWithSeverityTypeAndParent(t *testing.T) {
	t.Parallel()

	body := bodyFile(t)
	tests := []struct {
		name    string
		request model.CreateRequest
		want    []string
	}{
		{name: "plain", request: model.CreateRequest{Title: "T", Severity: model.SeverityP1}, want: []string{"create", "T", "-t", "task", "-p", "1"}},
		{name: "bug_is_a_type", request: model.CreateRequest{Title: "T", Severity: model.SeverityP0, Kind: "bug"}, want: []string{"create", "T", "-t", "bug", "-p", "0"}},
		{name: "feature_is_a_type", request: model.CreateRequest{Title: "T", Severity: model.SeverityP3, Kind: "feature"}, want: []string{"create", "T", "-t", "feature", "-p", "3"}},
		{name: "chore_is_a_type", request: model.CreateRequest{Title: "T", Severity: model.SeverityP2, Kind: "chore"}, want: []string{"create", "T", "-t", "chore", "-p", "2"}},
		{name: "task_is_a_type", request: model.CreateRequest{Title: "T", Severity: model.SeverityP2, Kind: "task"}, want: []string{"create", "T", "-t", "task", "-p", "2"}},
		{name: "other_kind_is_a_label", request: model.CreateRequest{Title: "T", Severity: model.SeverityP2, Kind: "docs"}, want: []string{"create", "T", "-t", "task", "-p", "2", "--body-file", body, "--json", "--labels", "docs"}},
		{name: "parent_and_assignee", request: model.CreateRequest{Title: "T", Severity: model.SeverityP2, Parent: "p-9", Assignee: "ann"}, want: []string{"create", "T", "-t", "task", "-p", "2", "--body-file", body, "--json", "--parent", "p-9", "--assignee", "ann"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bd, run := newBeads(t)
			run.plan("[]", "list")
			run.plan(`{"id":"p-42"}`, "create")
			tt.request.BodyFile = body

			id, err := bd.Create(context.Background(), tt.request)
			require.NoError(t, err)
			assert.Equal(t, "p-42", id)

			got := run.calls[len(run.calls)-1]
			if len(tt.want) == 6 {
				assert.Equal(t, tt.want, got[:6])
				assert.NotContains(t, got, "--labels")
			} else {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestBeadsCreate_DuplicateCheck(t *testing.T) {
	t.Parallel()

	body := bodyFile(t)
	bd, run := newBeads(t)
	run.plan(`[{"id":"p-7","title":"Same  Title"}]`, "list")
	run.plan(`{"id":"p-42"}`, "create")
	request := model.CreateRequest{Title: "same title", BodyFile: body, Severity: model.SeverityP2}

	_, err := bd.Create(context.Background(), request)
	require.EqualError(t, err, "REFUSED: p-7 is open with the same title. Comment there, or pass --allow-duplicate.")
	assert.Equal(t, store.CodeDuplicate, codeOf(t, err))
	assert.False(t, run.called("create"))
	assert.Equal(t, []string{"list", "--limit", "5000", "--json"}, run.calls[0])

	request.AllowDuplicate = true
	id, err := bd.Create(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, "p-42", id)
}

func TestBeadsCreate_Refuses(t *testing.T) {
	t.Parallel()

	body := bodyFile(t)
	tests := []struct {
		name  string
		setup func(*scripted)
		want  string
	}{
		{name: "list_fails", setup: func(r *scripted) { r.planFail("x", "list") }, want: "cannot read beads for the duplicate check; refusing to file"},
		{name: "full_page", setup: func(r *scripted) { r.plan(toJSON(t, fill(5000, nil)), "list") }, want: "5000 rows"},
		{name: "row_without_title", setup: func(r *scripted) { r.plan(`[{"id":"p-1"}]`, "list") }, want: "no readable title"},
		{name: "row_without_id", setup: func(r *scripted) { r.plan(`[{"title":"t"}]`, "list") }, want: "no readable id"},
		{name: "create_fails", setup: func(r *scripted) { r.plan("[]", "list"); r.planFail("x", "create") }, want: "bd create returned no id"},
		{name: "no_id", setup: func(r *scripted) { r.plan("[]", "list"); r.plan(`{"title":"x"}`, "create") }, want: "bd create returned no id"},
		{name: "not_json", setup: func(r *scripted) { r.plan("[]", "list"); r.plan("created", "create") }, want: "cannot read the answer of `bd create`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bd, run := newBeads(t)
			tt.setup(run)

			_, err := bd.Create(context.Background(), model.CreateRequest{Title: "T", BodyFile: body, Severity: model.SeverityP2})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, store.CodeRefused, codeOf(t, err))
		})
	}

	bd, _ := newBeads(t)
	_, err := bd.Create(context.Background(), model.CreateRequest{})
	require.ErrorContains(t, err, "create needs --title")
}
