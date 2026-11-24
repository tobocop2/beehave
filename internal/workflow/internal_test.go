package workflow

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

func TestPullRequests_SkipsAComputedMemberAndAComputedField(t *testing.T) {
	t.Parallel()

	member := starlarkstruct.FromKeywords(starlarkstruct.Default, []starlark.Tuple{
		{starlark.String("item"), computed{}}, {starlark.String("head"), computed{}}, {starlark.String("approved"), computed{}}, {starlark.String("closes"), computed{}},
	})

	got, err := PullRequests(starlark.NewList([]starlark.Value{computed{}, member}))
	require.NoError(t, err)
	assert.Equal(t, []PullRequest{{}}, got)

	_, err = PullRequests(starlark.NewList([]starlark.Value{computed{}, starlark.MakeInt(1)}))
	require.ErrorContains(t, err, "prs[1]: the member has the type int, not struct")
}

func TestComputed_IsAStarlarkValueWithNoHash(t *testing.T) {
	t.Parallel()

	var value starlark.Value = computed{}

	_, err := value.Hash()
	require.Error(t, err)
	assert.Equal(t, "computed", value.Type())
	assert.Equal(t, "<computed>", value.String())
	assert.Equal(t, starlark.True, value.Truth())
	value.Freeze()
}

func TestProblems_ReportsAnErrorOfAnotherKindAsText(t *testing.T) {
	t.Parallel()

	g := &graph{}
	g.problems(errors.New("the interpreter failed"))

	require.Len(t, g.found, 1)
	assert.Equal(t, "the interpreter failed", g.found[0].Text)
}
