package shell

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBlankFields_SplitsOnSpaceTabAndNewlineOnly(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"a", "b", "c d"}, blankFields(" a\tb\n\nc d "))
}

func TestFlatText_JoinsContinuedLinesAndFoldsBlanks(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "cargo test --all", flatText("cargo  test \\\n   --all"))
}
