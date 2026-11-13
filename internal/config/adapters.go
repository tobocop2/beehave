package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// ArgKind is the kind of one argument of an adapter block.
type ArgKind int

const (
	// TextArg is a string argument.
	TextArg ArgKind = iota
	// WholeArg is a whole-number argument.
	WholeArg
)

// Arg is one argument of an adapter block: its key, its kind and whether the call may leave it out.
type Arg struct {
	Key      string
	Kind     ArgKind
	Optional bool
}

// Block is the schema of an adapter block, as the adapter package declares it: the name of the call and its arguments.
type Block struct {
	Name string
	Args []Arg
}

// spec returns the loader schema of the block: the argument names with the optional mark.
func (b Block) spec() spec {
	args := make([]string, len(b.Args))
	for i, arg := range b.Args {
		args[i] = arg.Key
		if arg.Optional {
			args[i] += "?"
		}
	}

	return spec{b.Name, args}
}

// required says whether the block has an argument that the call must give.
func (b Block) required() bool {
	return slices.ContainsFunc(b.Args, func(arg Arg) bool { return !arg.Optional })
}

// Entry is one adapter of the registry: the block the adapter declares, the project keys it requires and the programs it needs on the path.
type Entry struct {
	// Block is the config block of the adapter. A nil Block declares none.
	Block *Block
	// Keys are the project keys that a project must set when it names the adapter, such as repo.
	Keys []string
	// Tools are the programs that the adapter needs on the path.
	Tools []string
	// Idle is true for a host provider that beehave deletes after idle_minutes.
	Idle bool
}

// Adapters is the registry of every adapter of a build: the names that the config accepts, by kind, with the block each name declares.
type Adapters struct {
	Trackers map[model.TrackerName]*Entry
	Claims   map[model.ClaimsName]*Entry
	Forges   map[model.ForgeName]*Entry
	Hosts    map[model.HostProvider]*Entry
	Agents   map[model.Agent]*Entry
}

// The default adapters of a project that names none.
const (
	defaultTracker = "local"
	defaultClaims  = "local"
)

// parseName returns the registered name that text names, or the refusal that lists the registered names.
func parseName[T ~string](registered map[T]*Entry, kind string, text string) (T, error) {
	if _, ok := registered[T(text)]; ok {
		return T(text), nil
	}

	return "", fmt.Errorf("unknown %s %q: use one of %s", kind, text, joinNames(registered))
}

func joinNames[T ~string](registered map[T]*Entry) string {
	names := make([]string, 0, len(registered))
	for name := range registered {
		names = append(names, string(name))
	}

	slices.Sort(names)

	return strings.Join(names, ", ")
}

// parseTracker returns the tracker that text names.
func (a Adapters) parseTracker(text string) (model.TrackerName, error) {
	return parseName(a.Trackers, "tracker", text)
}

// parseClaims returns the claim store that text names.
func (a Adapters) parseClaims(text string) (model.ClaimsName, error) {
	return parseName(a.Claims, "claims", text)
}

// parseForge returns the forge that text names.
func (a Adapters) parseForge(text string) (model.ForgeName, error) {
	return parseName(a.Forges, "forge", text)
}

// parseHostProvider returns the build host adapter that text names.
func (a Adapters) parseHostProvider(text string) (model.HostProvider, error) {
	return parseName(a.Hosts, "host provider", text)
}

// parseAgent returns the agent that text names.
func (a Adapters) parseAgent(text string) (model.Agent, error) {
	return parseName(a.Agents, "agent", text)
}

// projectBlocks returns the blocks that a project call holds: the blocks of the tracker, claim store and forge entries.
func (a Adapters) projectBlocks() ([]Block, error) {
	byName := map[string]Block{}

	for _, entries := range []map[string]*Entry{rekey(a.Trackers), rekey(a.Claims), rekey(a.Forges)} {
		if err := gatherBlocks(entries, byName); err != nil {
			return nil, err
		}
	}

	return sortedBlocks(byName), nil
}

// hostBlocks returns the blocks that a host call holds: the blocks of the host entries.
func (a Adapters) hostBlocks() ([]Block, error) {
	byName := map[string]Block{}

	if err := gatherBlocks(rekey(a.Hosts), byName); err != nil {
		return nil, err
	}

	return sortedBlocks(byName), nil
}

// rekey returns the entries of a kind keyed by the plain name, so one walk can read every kind.
func rekey[T ~string](registered map[T]*Entry) map[string]*Entry {
	plain := make(map[string]*Entry, len(registered))
	for name, entry := range registered {
		plain[string(name)] = entry
	}

	return plain
}

// gatherBlocks adds the blocks that the entries declare to byName, by block name. Two entries that declare the same
// name must declare the same block.
func gatherBlocks(entries map[string]*Entry, byName map[string]Block) error {
	for _, entry := range entries {
		if entry.Block == nil {
			continue
		}

		if known, seen := byName[entry.Block.Name]; seen && !sameBlock(known, *entry.Block) {
			return refuse("two adapters declare the `%s` block with different arguments. Fix the adapter table.", entry.Block.Name)
		}

		byName[entry.Block.Name] = *entry.Block
	}

	return nil
}

// sameBlock says whether two declared blocks have the same name and the same arguments.
func sameBlock(one, other Block) bool {
	return one.Name == other.Name && slices.Equal(one.Args, other.Args)
}

// sortedBlocks returns the blocks in name order.
func sortedBlocks(byName map[string]Block) []Block {
	blocks := make([]Block, 0, len(byName))
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		blocks = append(blocks, byName[name])
	}

	return blocks
}

// blockArgs returns the optional argument name of each block, in the order of the blocks.
func blockArgs(blocks []Block) []string {
	args := make([]string, len(blocks))
	for i, declared := range blocks {
		args[i] = declared.Name + "?"
	}

	return args
}
