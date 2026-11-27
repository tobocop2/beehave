package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/check/shell"
)

func TestIsTimeout_NamesBothTimeoutWrappers(t *testing.T) {
	t.Parallel()

	assert.True(t, shell.IsTimeout(shell.TimeoutName))
	assert.True(t, shell.IsTimeout(shell.GtimeoutName))
	assert.False(t, shell.IsTimeout(shell.SSHName))
}

func TestOneOf_FindsTheWordInTheOptions(t *testing.T) {
	t.Parallel()

	assert.True(t, shell.OneOf("b", "a", "b"))
	assert.False(t, shell.OneOf("c", "a", "b"))
	assert.False(t, shell.OneOf("c"))
}
