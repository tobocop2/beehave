package gate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQuote_KeepsASafeWordAndQuotesTheRest(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "abc-1.2/x=y", quote("abc-1.2/x=y"))
	assert.Equal(t, "''", quote(""))
	assert.Equal(t, "'a b'", quote("a b"))
	assert.Equal(t, `'it'\''s'`, quote("it's"))
	assert.Equal(t, `'$(x)'`, quote("$(x)"))
}
