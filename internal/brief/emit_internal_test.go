package brief

import (
	"embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFixed_ARefusalNamesTheMissingPartAndNoTextComesBack(t *testing.T) {
	t.Parallel()

	var w parts

	text := w.fixed(PartSkeleton, "missing")

	assert.Contains(t, text, "[SCRATCHPAD]")
	require.EqualError(t, w.err, "no brief text named missing: open text/missing.md: file does not exist")
}

func TestEmitAndWrite_RefuseWhenTheEmbeddedTextIsMissing(t *testing.T) { //nolint:paralleltest // the test swaps the embedded text for an empty one
	saved := texts
	texts = embed.FS{}

	t.Cleanup(func() { texts = saved })

	text, err := Emit(Spec{Kind: KindImpl})

	assert.Empty(t, text)
	require.ErrorContains(t, err, "no brief text named skeleton")

	text, err = write(Spec{Kind: KindImpl}, nil)

	assert.Empty(t, text)
	require.ErrorContains(t, err, "no brief text named skeleton")
}
