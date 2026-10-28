package papers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecordsOf_SplitsOnNewlineOnlyAndDropsTheLastEmptyRecord(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"a", "b\r", "c"}, recordsOf("a\nb\r\nc\n"))
	assert.Equal(t, []string{"a"}, recordsOf("a"))
}

func TestBlankFields_SplitsOnSpaceTabAndNewlineOnly(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"a", "b", "c d"}, blankFields(" a\tb\n\nc d "))
}
