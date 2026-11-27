package step_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/step"
	"github.com/tobocop2/beehest/internal/workflow"
)

func builtin(name string) step.Builtin { return step.Builtin{Name: name} }

func TestNewRegistry_RefusesTwoModulesThatNameTheSameBuiltin(t *testing.T) {
	t.Parallel()

	_, err := step.NewRegistry([]step.Module{
		{Name: "land", Builtins: []step.Builtin{builtin("merge")}},
		{Name: "extra", Builtins: []step.Builtin{builtin("merge")}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `module "land" and module "extra" both define the builtin "merge"`)
}

func TestNewRegistry_RefusesOneModuleThatNamesABuiltinTwice(t *testing.T) {
	t.Parallel()

	_, err := step.NewRegistry([]step.Module{{Name: "land", Builtins: []step.Builtin{builtin("merge"), builtin("merge")}}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `module "land" and module "land" both define the builtin "merge"`)
}

func TestNewRegistry_RefusesTwoModulesThatRecordTheSameStepKind(t *testing.T) {
	t.Parallel()

	_, err := step.NewRegistry([]step.Module{
		{Name: "land", Kinds: []string{"merge"}},
		{Name: "extra", Kinds: []string{"merge"}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `module "land" and module "extra" both record the step kind "merge"`)
}

func TestNewRegistry_ReportsEveryClashAtOnce(t *testing.T) {
	t.Parallel()

	_, err := step.NewRegistry([]step.Module{
		{Name: "a", Builtins: []step.Builtin{builtin("x")}, Kinds: []string{"x"}},
		{Name: "b", Builtins: []step.Builtin{builtin("x")}, Kinds: []string{"x"}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "builtin")
	assert.Contains(t, err.Error(), "step kind")
}

func TestRegistry_JoinsTheModulesInSortedOrder(t *testing.T) {
	t.Parallel()

	params := []workflow.Param{{Name: "name", Kind: workflow.KindText}}
	registry, err := step.NewRegistry([]step.Module{
		{Name: "b", Builtins: []step.Builtin{{Name: "zeta", Params: params, Results: []string{"out"}}}, Kinds: []string{"zeta", "alpha"}},
		{Name: "a", Builtins: []step.Builtin{builtin("beta"), {Name: "empty", Params: []workflow.Param{}}}, Kinds: []string{"mid"}},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"beta", "empty", "zeta"}, registry.Names())
	assert.Equal(t, []string{"alpha", "mid", "zeta"}, registry.Kinds())

	found, ok := registry.Builtin("zeta")
	require.True(t, ok)
	assert.Equal(t, "zeta", found.Name)

	_, ok = registry.Builtin("missing")
	assert.False(t, ok)
}

func TestRegistry_KindsReturnsACopy(t *testing.T) {
	t.Parallel()

	registry, err := step.NewRegistry([]step.Module{{Name: "a", Kinds: []string{"one"}}})
	require.NoError(t, err)

	registry.Kinds()[0] = "changed"

	assert.Equal(t, []string{"one"}, registry.Kinds())
}

func TestNewRegistry_AnEmptyListIsAnEmptyRegistry(t *testing.T) {
	t.Parallel()

	registry, err := step.NewRegistry(nil)

	require.NoError(t, err)
	assert.Empty(t, registry.Names())
	assert.Empty(t, registry.Kinds())
}
