package slurm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
)

// every returns a block that gives each key that the schema declares, so a key that the decode forgets shows as a missing value.
func every(t *testing.T) *model.Block {
	t.Helper()

	var entries []model.BlockEntry

	for _, arg := range Block().Args {
		switch {
		case arg.Kind == config.WholeArg:
			entries = append(entries, model.WholeEntry(arg.Key, 2222))
		case arg.Key == BlockIdentity || arg.Key == BlockWorkRoot:
			entries = append(entries, model.TextEntry(arg.Key, "/home/ci/"+arg.Key))
		case arg.Key == BlockKnownHosts:
			entries = append(entries, model.TextEntry(arg.Key, model.KnownHostsAcceptNew))
		default:
			entries = append(entries, model.TextEntry(arg.Key, arg.Key+"-value"))
		}
	}

	return model.NewBlock("p.star:3:4", entries)
}

func TestBlock_DeclaresTheKeysOfTheLinkAndOnlyOptionalOnes(t *testing.T) {
	t.Parallel()

	declared := Block()

	assert.Equal(t, "slurm", declared.Name)

	keys := make([]string, 0, len(declared.Args))
	for _, arg := range declared.Args {
		keys = append(keys, arg.Key)

		assert.True(t, arg.Optional, arg.Key)
	}

	assert.ElementsMatch(t, []string{"address", "user", "port", "identity", "work_root", "known_hosts"}, keys)
}

func TestDecodeBlock_ReadsEveryKeyTheBlockDeclares(t *testing.T) {
	t.Parallel()

	got, err := decodeBlock(every(t))

	require.NoError(t, err)
	assert.Equal(t, block{
		address: "address-value", user: "user-value", port: 2222, identity: "/home/ci/identity",
		workRoot: "/home/ci/work_root", knownHosts: model.KnownHostsAcceptNew,
	}, got)
}

func TestDecodeBlock_ANilBlockAndAnEmptyBlockGiveEveryKeyEmpty(t *testing.T) {
	t.Parallel()

	for name, from := range map[string]*model.Block{"nil": nil, "empty": model.NewBlock("p.star:1:1", nil)} {
		got, err := decodeBlock(from)

		require.NoError(t, err, name)
		assert.Equal(t, block{}, got, name)
	}
}

func TestDecodeBlock_RefusesAValueThatTheLinkCouldReadWrongAndNamesTheKeyAndThePosition(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		entry model.BlockEntry
		words string
	}{
		"known hosts empty":     {model.TextEntry(BlockKnownHosts, ""), "`host.slurm.known_hosts` must be strict or accept-new"},
		"port zero":             {model.WholeEntry(BlockPort, 0), "`host.slurm.port` must be from 1 to 65535; got 0"},
		"port negative":         {model.WholeEntry(BlockPort, -1), "got -1"},
		"port over the maximum": {model.WholeEntry(BlockPort, 65536), "got 65536"},
		"identity relative":     {model.TextEntry(BlockIdentity, "key"), "`host.slurm.identity` must be an absolute path or start with ~/; got `key`"},
		"identity with control": {model.TextEntry(BlockIdentity, "/a\nb"), "`host.slurm.identity` must hold no control character"},
	} {
		_, err := decodeBlock(model.NewBlock("p.star:3:4", []model.BlockEntry{tc.entry}))

		require.ErrorIs(t, err, link.ErrConfig, name)
		assert.Contains(t, err.Error(), tc.words, name)
		assert.Contains(t, err.Error(), "p.star:3:4", name)
	}
}

func TestDecodeBlock_TheEdgesOfThePortAndTheHomeRelativeIdentity(t *testing.T) {
	t.Parallel()

	for _, port := range []int{1, 65535} {
		got, err := decodeBlock(model.NewBlock("t", []model.BlockEntry{model.WholeEntry(BlockPort, port)}))

		require.NoError(t, err)
		assert.Equal(t, port, got.port)
	}

	got, err := decodeBlock(model.NewBlock("t", []model.BlockEntry{model.TextEntry(BlockIdentity, "~/.ssh/ci")}))

	require.NoError(t, err)
	assert.Equal(t, "~/.ssh/ci", got.identity)
}
