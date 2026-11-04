package sandbox_test

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/sandbox"
)

func TestOrDiscard_KeepsAWriterAndReplacesNil(t *testing.T) {
	t.Parallel()

	var out strings.Builder

	assert.Same(t, &out, sandbox.OrDiscard(&out))
	assert.Equal(t, io.Discard, sandbox.OrDiscard(nil))
}
