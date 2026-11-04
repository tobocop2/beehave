package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/model"
)

func TestNonEmptyLines_TrimsEachLineAndDropsTheBlankOnes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"a", "b c"}, model.NonEmptyLines(" a \n\n\t\nb c\n"))
	assert.Empty(t, model.NonEmptyLines("\n  \n"))
}
