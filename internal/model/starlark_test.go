package model_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func TestStarlarkFileOptionsAllowNoLoopAndNoRecursion(t *testing.T) {
	t.Parallel()

	options := model.StarlarkFileOptions()

	require.False(t, options.While)
	require.False(t, options.Recursion)
	require.False(t, options.TopLevelControl)
	require.Equal(t, 1_000_000, model.StarlarkMaxSteps)
}
