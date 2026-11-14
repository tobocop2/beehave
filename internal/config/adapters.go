package config

import (
	"errors"
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

// call returns the call that a project writes for the block, with its required arguments as placeholders: prefix = ...
func (b Block) call() string {
	var args []string

	for _, arg := range b.Args {
		if !arg.Optional {
			args = append(args, arg.Key+" = ...")
		}
	}

	return fmt.Sprintf("%s(%s)", b.Name, strings.Join(args, ", "))
}

// required says whether the block has an argument that the call must give.
func (b Block) required() bool {
	return slices.ContainsFunc(b.Args, func(arg Arg) bool { return !arg.Optional })
}

// Entry is one adapter of the registry: the block it declares, the project keys it requires, the programs it needs on the path and, for a host provider, how the config treats it.
type Entry struct {
	// Block is the config block of the adapter. A nil Block declares none.
	Block *Block
	// Keys are the project keys that a project must set when it names the adapter, such as repo.
	Keys []string
	// Tools are the programs that the adapter needs on the path.
	Tools []string
	// Idle is true for a host provider that beehave deletes after idle_minutes.
	Idle bool
	// HostBlock is the name of the nested block that a host provider takes in the host block, or empty for a provider that takes none.
	HostBlock string
}

// Adapters is the registry of every adapter of a build: the names that the config accepts, by kind, with the entry of each name.
type Adapters struct {
	Trackers map[model.TrackerName]*Entry
	Claims   map[model.ClaimsName]*Entry
	Forges   map[model.ForgeName]*Entry
	Hosts    map[model.HostProvider]*Entry
	Agents   map[model.Agent]*Entry
}

// The tracker and the claim store of a project that names none. The registry must hold an adapter of each name.
const (
	defaultTracker = "local"
	defaultClaims  = "local"
)

// entriesOf returns the entries of the named adapters in the order tracker, claims, forge. An empty forge name has no entry.
func (a Adapters) entriesOf(tracker model.TrackerName, claims model.ClaimsName, forge model.ForgeName) []*Entry {
	entries := []*Entry{a.Trackers[tracker], a.Claims[claims]}
	if forge != "" {
		entries = append(entries, a.Forges[forge])
	}

	return entries
}

// registeredNames returns the names of a registry map in sorted order.
func registeredNames[T ~string](registered map[T]*Entry) []string {
	names := make([]string, 0, len(registered))
	for name := range registered {
		names = append(names, string(name))
	}

	slices.Sort(names)

	return names
}

// parseName returns the text when it is one of the registered names, or the refusal that lists the registered names.
func parseName(registered []string, kind string, text string) (string, error) {
	if slices.Contains(registered, text) {
		return text, nil
	}

	return "", fmt.Errorf("unknown %s %q: use one of %s", kind, text, strings.Join(registered, ", "))
}

// parseTracker returns the tracker that text names.
func (a Adapters) parseTracker(text string) (model.TrackerName, error) {
	name, err := parseName(registeredNames(a.Trackers), "tracker", text)

	return model.TrackerName(name), err
}

// parseClaims returns the claim store that text names.
func (a Adapters) parseClaims(text string) (model.ClaimsName, error) {
	name, err := parseName(registeredNames(a.Claims), "claims", text)

	return model.ClaimsName(name), err
}

// parseForge returns the forge that text names.
func (a Adapters) parseForge(text string) (model.ForgeName, error) {
	name, err := parseName(registeredNames(a.Forges), "forge", text)

	return model.ForgeName(name), err
}

// parseHostProvider returns the build host adapter that text names.
func (a Adapters) parseHostProvider(text string) (model.HostProvider, error) {
	name, err := parseName(registeredNames(a.Hosts), "host provider", text)

	return model.HostProvider(name), err
}

// parseAgent returns the agent that text names.
func (a Adapters) parseAgent(text string) (model.Agent, error) {
	name, err := parseName(registeredNames(a.Agents), "agent", text)

	return model.Agent(name), err
}

// projectBlocks returns the blocks that a project call holds, by name order: the blocks that the tracker, claim store and forge entries declare.
// Two entries that declare one name must declare one schema.
func (a Adapters) projectBlocks() ([]Block, error) {
	byName := map[string]Block{}

	err := errors.Join(gatherBlocks(a.Trackers, byName), gatherBlocks(a.Claims, byName), gatherBlocks(a.Forges, byName))
	if err != nil {
		return nil, err
	}

	blocks := make([]Block, 0, len(byName))
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		blocks = append(blocks, byName[name])
	}

	return blocks, nil
}

// gatherBlocks adds the blocks that the entries declare to byName. Two entries that declare one name must declare the same arguments.
func gatherBlocks[T ~string](entries map[T]*Entry, byName map[string]Block) error {
	for _, entry := range entries {
		if entry.Block == nil {
			continue
		}

		declared := *entry.Block
		if known, seen := byName[declared.Name]; seen && !slices.Equal(known.Args, declared.Args) {
			return refuse("two adapters declare the `%s` block with different arguments. Report the adapters.", declared.Name)
		}

		byName[declared.Name] = declared
	}

	return nil
}
