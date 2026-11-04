package model_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/obee/internal/model"
)

func TestInnermostPosition_SkipsBuiltinFramesAndKeepsTheFirstFileFrame(t *testing.T) {
	t.Parallel()

	builtin, file, outer := "<builtin>", "p.star", "q.star"
	stack := starlark.CallStack{
		{Pos: syntax.MakePosition(&outer, 9, 9)},
		{Pos: syntax.MakePosition(&file, 3, 4)},
		{Pos: syntax.MakePosition(&builtin, 0, 0)},
		{Pos: syntax.MakePosition(&builtin, 0, 0)},
	}

	require.Equal(t, "p.star:3:4", model.InnermostPosition(stack).String())
}

func TestInnermostPosition_GivesNoPositionForAnEmptyStack(t *testing.T) {
	t.Parallel()

	require.False(t, model.InnermostPosition(nil).IsValid())
}

func TestStarlarkFileOptionsAllowNoLoopAndNoRecursion(t *testing.T) {
	t.Parallel()

	options := model.StarlarkFileOptions()

	require.False(t, options.While)
	require.False(t, options.Recursion)
	require.False(t, options.TopLevelControl)
	require.Equal(t, 1_000_000, model.StarlarkMaxSteps)
}
