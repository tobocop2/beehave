package skypilot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
)

func TestBlock_DeclaresThePlacementKeysAndOnlyOptionalOnes(t *testing.T) {
	t.Parallel()

	declared := Block()

	assert.Equal(t, "skypilot", declared.Name)

	kinds := map[string]config.ArgKind{}

	for _, arg := range declared.Args {
		kinds[arg.Key] = arg.Kind

		assert.True(t, arg.Optional, arg.Key)
	}

	assert.Equal(t, map[string]config.ArgKind{
		"cloud": config.TextArg, "region": config.TextArg, "instance_type": config.TextArg, "instance_sizes": config.TextArg,
		"disk_size_gb": config.WholeArg, "spot": config.BoolArg,
	}, kinds)
}

func TestDecodeBlock_ReadsEveryKeyTheBlockDeclares(t *testing.T) {
	t.Parallel()

	entries := make([]model.BlockEntry, 0, len(Block().Args))

	for _, arg := range Block().Args {
		switch arg.Kind {
		case config.WholeArg:
			entries = append(entries, model.WholeEntry(arg.Key, 100))
		case config.BoolArg:
			entries = append(entries, model.BoolEntry(arg.Key, true))
		case config.TextArg:
			entries = append(entries, model.TextEntry(arg.Key, arg.Key+"-value"))
		}
	}

	got := decodeBlock(model.NewBlock("p.star:3:4", entries))

	assert.Equal(t, block{cloud: "cloud-value", region: "region-value", instanceType: "instance_type-value", instanceSizes: "instance_sizes-value", diskSizeGB: 100, diskSized: true, spot: true}, got)
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

func TestNew_RefusesADiskSizeOutsideTenToSixtyFiveThousandFiveHundredThirtySixAndNamesTheKey(t *testing.T) {
	t.Parallel()

	for _, size := range []int{-1, 5, 9, 65537} {
		_, err := New("c", hostConfig(&placed{diskSizeGB: size}), nil, nil, nil)

		require.ErrorIs(t, err, ErrConfig, size)
		require.ErrorContains(t, err, "`host.skypilot.disk_size_gb` must be a whole number from 10 to 65536", size)
	}

	for _, size := range []int{10, 65536} {
		_, err := New("c", hostConfig(&placed{diskSizeGB: size}), nil, nil, func(*link.Link, model.HostConfig, Slot) Queue { return &fakeQueue{} })

		require.NoError(t, err, size)
	}
}

func TestNew_ADiskSizeOfZeroThatTheProjectWroteIsRefused(t *testing.T) {
	t.Parallel()

	zero := hostConfig(nil)
	zero.Block = model.NewBlock("p.star:1:1", []model.BlockEntry{model.WholeEntry(BlockDiskSizeGB, 0)})

	_, err := New("c", zero, nil, nil, nil)

	require.ErrorIs(t, err, ErrConfig)
	assert.ErrorContains(t, err, "disk_size_gb")
}
