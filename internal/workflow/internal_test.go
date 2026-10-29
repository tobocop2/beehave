package workflow

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProblems_ReportsAnErrorOfAnotherKindAsText(t *testing.T) {
	t.Parallel()

	g := &graph{}
	g.problems(errors.New("the interpreter failed"))

	require.Len(t, g.found, 1)
	assert.Equal(t, "the interpreter failed", g.found[0].Text)
}
