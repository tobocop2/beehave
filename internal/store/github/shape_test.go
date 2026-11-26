package github_test

import (
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ghcontract"
	"github.com/tobocop2/beehave/internal/store"
	"github.com/tobocop2/beehave/internal/store/github"
)

func TestStore_EveryCallShapeIsOneGhAccepts(t *testing.T) {
	t.Parallel()

	fake := newFakeGh()
	s := writerOver(t, fake)
	ctx := t.Context()

	var driven []string

	id, _, err := s.Create(ctx, "key-1", store.NewItem{Title: "title", Body: "body", Parent: "3", Assignee: "me", Severity: "high", Operator: true})
	require.NoError(t, err)

	_, err = s.Item(ctx, id)
	require.NoError(t, err)

	_, err = s.ItemList(ctx, "operator")
	require.NoError(t, err)

	_, err = s.Comments(ctx, id)
	require.NoError(t, err)

	_, _ = s.Parent(ctx, id)

	for _, status := range []store.Status{store.StatusHold, store.StatusOpen, store.StatusClosed, store.StatusOpen} {
		require.NoError(t, s.SetStatus(ctx, id, status))
	}

	require.NoError(t, s.Comment(ctx, id, "key-2", "text"))

	driven = append(driven, "Create", "Item", "ItemList", "Comments", "Parent", "SetStatus", "Comment", "ItemFields")
	assert.NotEmpty(t, s.ItemFields())

	var methods []string

	kind := reflect.TypeFor[*github.Store]()
	for i := range kind.NumMethod() {
		methods = append(methods, kind.Method(i).Name)
	}

	sort.Strings(driven)
	assert.Equal(t, methods, driven, "every public method of the store needs a call here, so a new method cannot skip the contract")

	for _, call := range fake.calls {
		stderr, ok := ghcontract.Check(call)

		assert.True(t, ok, "the store sent gh %v: %s", call, stderr)
	}

	for _, flag := range []string{"--parent", "--assignee", "--label", "--add-label", "--remove-label", "--search", "--paginate"} {
		assert.True(t, slices.ContainsFunc(fake.calls, func(call []string) bool { return slices.Contains(call, flag) }), "no call of the store had %s: the test does not reach it", flag)
	}
}
