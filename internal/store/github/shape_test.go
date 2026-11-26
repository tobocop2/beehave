package github_test

import (
	"slices"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ghcontract"
	"github.com/tobocop2/beehave/internal/store"
	"github.com/tobocop2/beehave/internal/store/github"
)

// shapeNoCall are the methods that answer without a gh call.
var shapeNoCall = []string{"ItemFields"}

// shapeDrivers calls every public method of the store, each in a driver of its own. The map is the list of the methods that the test drives: a method with no driver fails the test,
// and so does a driver whose method makes no gh call. Create runs first, because the other drivers read the issue it makes.
func shapeDrivers(t *testing.T, s *github.Store) map[string]func() {
	t.Helper()

	ctx := t.Context()

	var id string

	return map[string]func(){
		"Create": func() {
			var err error

			id, _, err = s.Create(ctx, "key-1", store.NewItem{Title: "title", Body: "body", Parent: "3", Assignee: "me", Severity: "high", Operator: true})
			require.NoError(t, err)
		},
		"Item": func() {
			_, err := s.Item(ctx, id)
			require.NoError(t, err)
		},
		"ItemList": func() {
			_, err := s.ItemList(ctx, "operator")
			require.NoError(t, err)
		},
		"Comments": func() {
			_, err := s.Comments(ctx, id)
			require.NoError(t, err)
		},
		"Parent": func() { t.Log(s.Parent(ctx, id)) },
		"SetStatus": func() {
			for _, status := range []store.Status{store.StatusHold, store.StatusOpen, store.StatusClosed, store.StatusOpen} {
				require.NoError(t, s.SetStatus(ctx, id, status))
			}
		},
		"Comment":    func() { require.NoError(t, s.Comment(ctx, id, "key-2", "text")) },
		"ItemFields": func() { assert.NotEmpty(t, s.ItemFields()) },
	}
}

func TestStore_EveryCallShapeIsOneGhAccepts(t *testing.T) {
	t.Parallel()

	fake := newFakeGh()
	drivers := shapeDrivers(t, writerOver(t, fake))

	var names []string

	for name := range drivers {
		names = append(names, name)
	}

	sort.Strings(names)

	methods, err := ghcontract.PublicMethods(".", "Store")
	require.NoError(t, err)
	require.Equal(t, methods, names, "every public method of the store needs a driver here, so a new method cannot skip the contract")

	order := append([]string{"Create"}, slices.DeleteFunc(slices.Clone(names), func(name string) bool { return name == "Create" })...)

	for _, name := range order {
		before := len(fake.calls)

		drivers[name]()

		if !slices.Contains(shapeNoCall, name) {
			assert.Greater(t, len(fake.calls), before, "the driver of %s made no gh call: the method skips the contract", name)
		}
	}

	for _, call := range fake.calls {
		stderr, ok := ghcontract.Check(call)

		assert.True(t, ok, "the store sent gh %v: %s", call, stderr)
	}

	for _, flag := range []string{"--parent", "--assignee", "--label", "--add-label", "--remove-label", "--search", "--paginate"} {
		assert.True(t, slices.ContainsFunc(fake.calls, func(call []string) bool { return slices.Contains(call, flag) }), "no call of the store had %s: the test does not reach it", flag)
	}
}
