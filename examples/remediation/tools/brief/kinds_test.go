package brief_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/brief"
)

func TestParseKind_ReturnsEachKindByItsName(t *testing.T) {
	t.Parallel()

	for _, want := range brief.Kinds() {
		got, err := brief.ParseKind(string(want))

		require.NoError(t, err)
		assert.Equal(t, want, got)
	}

	assert.Equal(t, []brief.Kind{brief.KindImpl, brief.KindRev, brief.KindDesign, brief.KindPapers}, brief.Kinds())
}

func TestParseKind_RefusesAnUnknownKindAndListsTheKinds(t *testing.T) {
	t.Parallel()

	_, err := brief.ParseKind("bake")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "'bake' is not a kind of brief. The kinds are impl, rev, design and papers.")
}
