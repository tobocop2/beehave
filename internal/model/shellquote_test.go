package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestShellQuote_KeepsASafeWordAndQuotesTheRest(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "abc-1.2/x=y", model.ShellQuote("abc-1.2/x=y"))
	assert.Equal(t, "''", model.ShellQuote(""))
	assert.Equal(t, "'a b'", model.ShellQuote("a b"))
	assert.Equal(t, `'it'\''s'`, model.ShellQuote("it's"))
	assert.Equal(t, `'$(x)'`, model.ShellQuote("$(x)"))
}
