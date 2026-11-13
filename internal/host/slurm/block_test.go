package slurm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

func TestBlock_DeclaresTheKeysOfTheLinkAllOptional(t *testing.T) {
	t.Parallel()

	schema := Block()

	var keys []string

	for _, arg := range schema.Args {
		assert.True(t, arg.Optional, arg.Key)

		keys = append(keys, arg.Key)
	}

	assert.Equal(t, "slurm", schema.Name)
	assert.ElementsMatch(t, []string{BlockAddress, BlockUser, BlockPort, BlockIdentity, BlockWorkRoot, BlockKnownHosts}, keys)
}

func TestDecodeBlock_ReadsEveryKey(t *testing.T) {
	t.Parallel()

	got, err := decodeBlock(model.NewBlock("t", []model.BlockEntry{
		model.TextEntry(BlockAddress, "head.test"), model.TextEntry(BlockUser, "lane"), model.WholeEntry(BlockPort, 2222),
		model.TextEntry(BlockIdentity, "~/.ssh/id"), model.TextEntry(BlockWorkRoot, "/work"), model.TextEntry(BlockKnownHosts, "strict"),
	}))

	require.NoError(t, err)
	assert.Equal(t, block{address: "head.test", user: "lane", port: 2222, identity: "~/.ssh/id", workRoot: "/work", knownHosts: "strict"}, got)
}

func TestDecodeBlock_ANilBlockAndABlockWithNoEntryGiveEveryKeyEmpty(t *testing.T) {
	t.Parallel()

	for _, from := range []*model.Block{nil, model.NewBlock("t", nil)} {
		got, err := decodeBlock(from)

		require.NoError(t, err)
		assert.Equal(t, block{}, got)
	}
}

func TestDecodeBlock_RefusesWhatTheLinkCannotTake(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		entry   model.BlockEntry
		refused string
	}{
		"port_zero":          {model.WholeEntry(BlockPort, 0), "from 1 to 65535"},
		"port_above":         {model.WholeEntry(BlockPort, 65536), "from 1 to 65535"},
		"identity_relative":  {model.TextEntry(BlockIdentity, "id_ed25519"), "absolute path"},
		"identity_control":   {model.TextEntry(BlockIdentity, "/home/a\nb"), "no control character"},
		"work_root_control":  {model.TextEntry(BlockWorkRoot, "/work\x00"), "no control character"},
		"work_root_relative": {model.TextEntry(BlockWorkRoot, "work"), "absolute path"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := decodeBlock(model.NewBlock("t", []model.BlockEntry{tt.entry}))

			require.ErrorIs(t, err, link.ErrConfig)
			require.ErrorContains(t, err, tt.refused)
		})
	}
}

func TestNewHost_RefusesABlockThatDoesNotDecode(t *testing.T) {
	t.Parallel()

	cfg := model.HostConfig{MaxRun: time.Hour, Block: model.NewBlock("t", []model.BlockEntry{model.WholeEntry(BlockPort, 0)})}

	_, err := NewHost("gates", cfg, proctest.Always(model.CommandResult{}))

	require.ErrorIs(t, err, link.ErrConfig)
}
