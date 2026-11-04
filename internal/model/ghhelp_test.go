package model_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
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
