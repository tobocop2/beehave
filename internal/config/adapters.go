package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// Entry is one adapter of the registry: the project keys it requires, the programs it needs on the path and, for a host provider, how the config treats it.
type Entry struct {
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
