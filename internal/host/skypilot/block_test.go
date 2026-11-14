package skypilot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestBlock_DeclaresThePlacementKeysAndOnlyOptionalOnes(t *testing.T) {
	t.Parallel()

	declared := Block()

	assert.Equal(t, "skypilot", declared.Name)

	keys := make([]string, 0, len(declared.Args))
	for _, arg := range declared.Args {
		keys = append(keys, arg.Key)

		assert.True(t, arg.Optional, arg.Key)
	}

	assert.ElementsMatch(t, []string{"cloud", "region", "instance_type"}, keys)
}

func TestDecodeBlock_ReadsEveryKeyTheBlockDeclares(t *testing.T) {
	t.Parallel()

	entries := make([]model.BlockEntry, 0, len(Block().Args))
	for _, arg := range Block().Args {
		entries = append(entries, model.TextEntry(arg.Key, arg.Key+"-value"))
	}

	got := decodeBlock(model.NewBlock("p.star:3:4", entries))

	assert.Equal(t, block{cloud: "cloud-value", region: "region-value", instanceType: "instance_type-value"}, got)
}

func TestDecodeBlock_ANilBlockAndAnEmptyBlockLeaveEveryChoiceToSkyPilot(t *testing.T) {
	t.Parallel()

	assert.Equal(t, block{}, decodeBlock(nil))
	assert.Equal(t, block{}, decodeBlock(model.NewBlock("p.star:1:1", nil)))
}

func TestNew_ReadsTheBlockOnceAndPassesItsPlacementToSkyLaunch(t *testing.T) {
	t.Parallel()

	h, _, _, _ := newHost(t, hostConfig(&placed{cloud: "gcp", region: "us-central1", instanceType: "n2-standard-8"}))

	assert.Equal(t, block{cloud: "gcp", region: "us-central1", instanceType: "n2-standard-8"}, h.values)
	assert.Equal(t, "gcp/us-central1", h.infra())
}

func TestNew_RefusesAPlacementWordThatSkyCouldReadAsAnOptionAndNamesTheKey(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		placement placed
		words     string
	}{
		"cloud":         {placed{cloud: "-x"}, "host.skypilot.cloud"},
		"region":        {placed{cloud: "gcp", region: "a b"}, "host.skypilot.region"},
		"instance type": {placed{instanceType: "--x"}, "host.skypilot.instance_type"},
	} {
		_, err := New("c", hostConfig(&tc.placement), nil, nil, nil)

		require.ErrorIs(t, err, ErrConfig, name)
		assert.Contains(t, err.Error(), tc.words, name)
	}
}
