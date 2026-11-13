package config

import (
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	defaultIdleMins   = 60
	defaultStaleMins  = 20
	maxCount          = model.MaxDiskFloorGB
	defaultMaxRun     = "24h"
	defaultBankBudget = "20s"
	hostTableMessage  = "the `host` block %s. %s"
	hostSpecName      = "host"
)

// rawHost is a host block as written. At is the position of its call. Blocks holds the provider blocks that the call holds, by block name.
type rawHost struct {
	At            syntax.Position
	Provider      string
	IdleMinutes   int
	StaleMinutes  int
	MaxRun        string
	BankBudget    string
	DiskFloorGB   int
	DiskWaitMins  int
	PrivateRemote string
	Blocks        map[string]*model.Block
}

// The schema of the host constructor: the keys every provider shares, and one optional argument for each provider block.
func hostSpec(blocks []Block) spec {
	args := append([]string{"provider", "disk_floor_gb", "idle_minutes?", "stale_minutes?", "max_run?", "disk_wait_minutes?", "bank_budget?", "private_remote?"},
		blockArgs(blocks)...)

	return spec{hostSpecName, args}
}

// idleUnset is the value of a key that the call leaves out, so that the loader can tell an omitted key from a written one.
const idleUnset = -1 << 31

// newHost is the constructor of the host block. A key that the call leaves out keeps its default.
func newHost(blocks []Block) func(at syntax.Position, kwargs []starlark.Tuple) (*rawHost, error) {
	return func(at syntax.Position, kwargs []starlark.Tuple) (*rawHost, error) {
		raw := &rawHost{At: at, IdleMinutes: idleUnset, StaleMinutes: defaultStaleMins, MaxRun: defaultMaxRun, BankBudget: defaultBankBudget, Blocks: map[string]*model.Block{}}

		dests := make([]*nested[model.Block], len(blocks))
		for i, declared := range blocks {
			dests[i] = &nested[model.Block]{want: declared.Name}
		}

		held := []any{&raw.Provider, &raw.DiskFloorGB, &raw.IdleMinutes, &raw.StaleMinutes, &raw.MaxRun, &raw.DiskWaitMins, &raw.BankBudget, &raw.PrivateRemote}
		for _, dest := range dests {
			held = append(held, dest)
		}

		err := hostSpec(blocks).unpack(at, kwargs, held...)

		for i, declared := range blocks {
			if dests[i].block != nil {
				raw.Blocks[declared.Name] = dests[i].block
			}
		}

		return raw, err
	}
}

// hostConfig checks the host block against build_host and returns it. A project that builds locally has none.
// A block that must go points at itself. A block that is missing points at the project call.
func hostConfig(buildHost string, project rawProject, registry Adapters) (*model.HostConfig, error) {
	raw := project.Host

	switch {
	case buildHost == LocalHost && raw == nil:
		return nil, nil //nolint:nilnil // a local build has no host table
	case buildHost == LocalHost:
		return nil, refuseAt(raw.At, hostTableMessage, "is not allowed when `build_host` is \"local\"", "Remove the block, or set `build_host` to a cluster name.")
	case raw == nil:
		return nil, refuseAt(project.At, hostTableMessage, "is required when `build_host` is a cluster name", "Add a `host` block with `provider` and `disk_floor_gb`.")
	}

	blocks, err := registry.hostBlocks()
	if err != nil {
		return nil, err
	}

	return buildHostConfig(*raw, registry, blocks)
}

func buildHostConfig(raw rawHost, registry Adapters, blocks []Block) (*model.HostConfig, error) {
	provider, err := parseKey(raw.At, "host.provider", raw.Provider, registry.parseHostProvider)
	if err != nil {
		return nil, err
	}

	entry := registry.Hosts[provider]

	if err = checkBlocks(raw, entry, blocks); err != nil {
		return nil, err
	}

	host := model.HostConfig{Provider: provider, PrivateRemote: raw.PrivateRemote}

	if entry.Block != nil {
		host.Block = raw.Blocks[entry.Block.Name]
	}

	// Only a provider that beehave deletes gets a default for idle_minutes; fillCounts replaces it with a written value.
	if entry.Idle {
		host.IdleMinutes = defaultIdleMins
	}

	if err = fillKeys(raw, &host); err != nil {
		return nil, err
	}

	return &host, nil
}

// checkBlocks refuses a block that the chosen provider does not take, and idle_minutes for a provider that beehave does not delete the machine of.
func checkBlocks(raw rawHost, entry *Entry, blocks []Block) error {
	for _, declared := range blocks {
		if entry.Block != nil && declared.Name == entry.Block.Name {
			continue
		}

		if _, held := raw.Blocks[declared.Name]; held {
			return refuseAt(raw.At, "`host.provider` %q takes no `%s` block. Remove the `%s` block.", raw.Provider, declared.Name, declared.Name)
		}
	}

	if !entry.Idle && raw.IdleMinutes != idleUnset {
		return refuseAt(raw.At, "`host.idle_minutes` is not allowed for provider %q: beehave does not delete the machine. Remove the key.", raw.Provider)
	}

	return nil
}

// fillKeys sets the keys of host that are not the provider name.
func fillKeys(raw rawHost, host *model.HostConfig) error {
	var err error

	if raw.PrivateRemote != "" && !model.RemoteHasHost(raw.PrivateRemote) {
		return refuseAt(raw.At, "`host.private_remote` must name a machine, such as git@host:org/repo.git; a path or a file URL does not count. Fix the value.")
	}

	if err = fillCounts(raw, host); err != nil {
		return err
	}

	if host.MaxRun, err = duration(raw.At, "host.max_run", raw.MaxRun, defaultMaxRun); err != nil {
		return err
	}

	host.BankBudget, err = duration(raw.At, "host.bank_budget", raw.BankBudget, defaultBankBudget)

	return err
}

// fillCounts sets the keys of host that are whole numbers.
func fillCounts(raw rawHost, host *model.HostConfig) error {
	var err error

	if raw.IdleMinutes != idleUnset {
		if host.IdleMinutes, err = positive(raw.At, "host.idle_minutes", raw.IdleMinutes); err != nil {
			return err
		}
	}

	if host.StaleMinutes, err = positive(raw.At, "host.stale_minutes", raw.StaleMinutes); err != nil {
		return err
	}

	if raw.DiskWaitMins < 0 || raw.DiskWaitMins > maxCount {
		return refuseAt(raw.At, "`host.disk_wait_minutes` must be zero or above and at most %d; got %d. Fix the value.", maxCount, raw.DiskWaitMins)
	}

	host.DiskWaitMinutes = raw.DiskWaitMins
	host.DiskFloorGB, err = positive(raw.At, "host.disk_floor_gb", raw.DiskFloorGB)

	return err
}

// positive checks the value of an integer key.
func positive(at syntax.Position, key string, value int) (int, error) {
	if value <= 0 || value > maxCount {
		return 0, refuseAt(at, "`%s` must be above zero and at most %d; got %d. Fix the value.", key, maxCount, value)
	}

	return value, nil
}

// duration parses the text of a duration key. The example in the refusal is the default of the key.
func duration(at syntax.Position, key, text, example string) (time.Duration, error) {
	parsed, err := time.ParseDuration(text)
	if err != nil || parsed <= 0 {
		return 0, refuseAt(at, "`%s` must be a duration above zero, such as \"%s\"; got `%s`. Fix the value.", key, example, text)
	}

	return parsed, nil
}
