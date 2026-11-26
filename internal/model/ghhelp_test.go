package model_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestDecodePages_JoinsThePagesAndRefusesANullPageOrItem(t *testing.T) {
	t.Parallel()

	got, err := model.DecodePages[int]("gh api", "[[1,2],[],[3]]")
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, got)

	empty, err := model.DecodePages[int]("gh api", "[]")
	require.NoError(t, err)
	require.NotNil(t, empty)
	require.Empty(t, empty)

	_, err = model.DecodePages[int]("gh api", "[[1],null]")
	require.ErrorContains(t, err, "a page or an item is null")

	_, err = model.DecodePages[int]("gh api", "[[1,null]]")
	require.ErrorContains(t, err, "a page or an item is null")

	_, err = model.DecodePages[int]("gh api", "null")
	require.ErrorIs(t, err, model.ErrNullAnswer)
}

func TestDecodeGraphPages_JoinsTheListsOfThePagesAndRefusesACutOrMissingList(t *testing.T) {
	t.Parallel()

	repository := `[{"data":{"repository":{"list":{"totalCount":3,"nodes":[1,2]}}}},{"data":{"repository":{"list":{"totalCount":3,"nodes":[3]}}}}]`
	got, err := model.DecodeGraphPages[int]("gh api graphql", repository)
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, got, "the pages of a list under the repository join in order")

	top, err := model.DecodeGraphPages[int]("gh api graphql", `[{"data":{"list":{"totalCount":1,"nodes":[7]}}}]`)
	require.NoError(t, err)
	require.Equal(t, []int{7}, top, "a list at the top of the answer, such as a search, reads the same")

	empty, err := model.DecodeGraphPages[int]("gh api graphql", `[{"data":{"repository":{"list":{"totalCount":0,"nodes":[]}}}}]`)
	require.NoError(t, err)
	require.NotNil(t, empty)
	require.Empty(t, empty)

	_, err = model.DecodeGraphPages[int]("gh api graphql", `[{"data":{"repository":{"list":{"totalCount":5,"nodes":[1,2]}}}}]`)
	require.ErrorIs(t, err, model.ErrMorePages, "fewer rows than the total is a cut list")
	require.ErrorContains(t, err, "2 of 5 rows, so the rest are unknown")

	_, err = model.DecodeGraphPages[int]("gh api graphql", `[{"data":{"list":{"totalCount":1,"nodes":[1]}}},{"data":{"list":{"totalCount":1,"nodes":[2]}}}]`)
	require.ErrorContains(t, err, "2 rows, more than the total of 1", "more rows than the total is an answer that does not add up")
	require.NotErrorIs(t, err, model.ErrMorePages)

	later, err := model.DecodeGraphPages[int]("gh api graphql", `[{"data":{"list":{"totalCount":2,"nodes":[1]}}},{"data":{"list":{"totalCount":9,"nodes":[2]}}}]`)
	require.NoError(t, err, "the total of the first page is the total of the list")
	require.Equal(t, []int{1, 2}, later)

	for _, shape := range []string{`[]`, `[{"data":null}]`, `[{"data":{"repository":{}}}]`, `[{"data":{}}]`, `[{}]`} {
		_, err = model.DecodeGraphPages[int]("gh api graphql", shape)
		require.ErrorContains(t, err, "a page holds no list", shape)
	}

	_, err = model.DecodeGraphPages[int]("gh api graphql", "null")
	require.ErrorIs(t, err, model.ErrNullAnswer)
}

func TestURLNumber_ReadsTheNumberAfterTheKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		text string
		kind string
		want int
		ok   bool
	}{
		{"https://github.com/o/r/pull/42\n", "pull", 42, true},
		{"warning\nhttps://github.com/o/r/issues/7", "issues", 7, true},
		{"https://github.com/o/r/issues/7", "pull", 0, false},
		{"https://github.com/o/r/pull/0", "pull", 0, false},
		{"https://github.com/o/r/pull/+5", "pull", 0, false},
		{"https://github.com/o/r/pull/99999999999999999999", "pull", 0, false},
		{"7", "issues", 0, false},
		{"", "issues", 0, false},
	}

	for _, tc := range tests {
		got, ok := model.URLNumber(tc.text, tc.kind)
		require.Equal(t, tc.ok, ok, tc.text)
		require.Equal(t, tc.want, got, tc.text)
	}
}

func TestSplitRepoAndDeref(t *testing.T) {
	t.Parallel()

	owner, name, ok := model.SplitRepo("o/r")
	require.True(t, ok)
	require.Equal(t, "o", owner)
	require.Equal(t, "r", name)

	for _, bad := range []string{"", "o", "o/", "/r", "o/r/x", "o/..", "o/r s"} {
		_, _, ok = model.SplitRepo(bad)
		require.False(t, ok, bad)
	}

	text := "x"
	require.Equal(t, "x", model.Deref(&text))
	require.Empty(t, model.Deref(nil))
}
