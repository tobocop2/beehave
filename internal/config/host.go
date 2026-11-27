package config

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehest/internal/model"
)

const (
	defaultIdleMins   = 60
	defaultStaleMins  = 20
	maxCount          = model.MaxDiskFloorGB
	defaultMaxRun     = "24h"
	minMaxRun         = time.Second
	defaultBankBudget = "20s"
	// DefaultLostJobWait is the default of host.lost_job_wait. The engine also waits this long for a project with no host block.
	// The bounds follow it. The lower bound is one poll of the wait.
	DefaultLostJobWait = 60 * time.Minute
	// DefaultLostAfter is the default of host.lost_after: the time without an answered read before a wait for a job ends as lost.
	DefaultLostAfter = 5 * time.Minute
	// DefaultStopLimit is the default of host.stop_limit: the time a job may take to end after the cancel.
	DefaultStopLimit = 5 * time.Minute
	minLostJobWait   = time.Minute
	maxLostJobWait   = 24 * time.Hour
	hostTableMessage = "the `host` block %s. %s"
	hostSpecName     = "host"
)

// rawHost is a host block as written. At is the position of its call. Blocks holds the provider blocks that the call holds, by block name.
type rawHost struct {
	At            syntax.Position
	Provider      string
	IdleMinutes   int
	StaleMinutes  int
	MaxRun        string
	BankBudget    string
	LostJobWait   string
	LostAfter     string
	StopLimit     string
	DiskFloorGB   int
	DiskWaitMins  int
	PrivateRemote string
	Slots         slotTable
	GateSlots     int
	Credentials   text
	SetupCommand  text
	Blocks        map[string]*model.Block
}

// hostSpec is the schema of the host constructor: the keys every provider shares, and one optional argument for each provider block that the registry declares.
func hostSpec(declared []Block) spec {
	args := []string{"provider", "disk_floor_gb", "idle_minutes?", "stale_minutes?", "max_run?", "disk_wait_minutes?", "bank_budget?", "lost_job_wait?", "slots?", "gate_slots?", "private_remote?", "lost_after?", "stop_limit?", "credentials?", "setup_command?"}

	return spec{hostSpecName, append(args, blockArgs(declared)...)}
}

// idleUnset is the value of a key that the call leaves out, so that the loader can tell an omitted key from a written one.
const idleUnset = -1 << 31

// newHost returns the constructor of the host block. A key that the call leaves out keeps its default.
func newHost(declared []Block) func(at syntax.Position, kwargs []starlark.Tuple) (*rawHost, error) {
	return func(at syntax.Position, kwargs []starlark.Tuple) (*rawHost, error) {
		raw := &rawHost{At: at, GateSlots: 1, IdleMinutes: idleUnset, StaleMinutes: defaultStaleMins, MaxRun: defaultMaxRun, BankBudget: defaultBankBudget, LostJobWait: defaultLostJobWaitText(), LostAfter: minutesText(DefaultLostAfter), StopLimit: minutesText(DefaultStopLimit)}
		held := holders(declared)
		dests := []any{&raw.Provider, &raw.DiskFloorGB, &raw.IdleMinutes, &raw.StaleMinutes, &raw.MaxRun, &raw.DiskWaitMins, &raw.BankBudget, &raw.LostJobWait, &raw.Slots, &raw.GateSlots, &raw.PrivateRemote, &raw.LostAfter, &raw.StopLimit, &raw.Credentials, &raw.SetupCommand}

		err := hostSpec(declared).unpack(at, kwargs, append(dests, blockDests(held)...)...)
		raw.Blocks = heldBlocks(declared, held)

		return raw, err
	}
}

// holders returns one empty holder for each declared block, to receive the block of a call.
func holders(declared []Block) []nested[model.Block] {
	held := make([]nested[model.Block], len(declared))

	for i, block := range declared {
		held[i].want = block.Name
	}

	return held
}

// blockDests returns the holders as the destinations of a call.
func blockDests(held []nested[model.Block]) []any {
	dests := make([]any, len(held))

	for i := range held {
		dests[i] = &held[i]
	}

	return dests
}

// heldBlocks returns the blocks that the call gave, by block name.
func heldBlocks(declared []Block, held []nested[model.Block]) map[string]*model.Block {
	given := map[string]*model.Block{}

	for i, block := range declared {
		if held[i].block != nil {
			given[block.Name] = held[i].block
		}
	}

	return given
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

	return buildHostConfig(*raw, registry)
}

func buildHostConfig(raw rawHost, registry Adapters) (*model.HostConfig, error) {
	provider, err := parseKey(raw.At, "host.provider", raw.Provider, registry.parseHostProvider)
	if err != nil {
		return nil, err
	}

	entry := registry.Hosts[provider]

	if err = checkBlocks(raw, provider, entry); err != nil {
		return nil, err
	}

	host := startHost(raw, provider, entry)

	if err = fillKeys(raw, &host); err != nil {
		return nil, err
	}

	return &host, nil
}

// startHost returns the host with the provider, the remote, the block of the provider and the defaults that depend on the provider.
func startHost(raw rawHost, provider model.HostProvider, entry *Entry) model.HostConfig {
	host := model.HostConfig{Provider: provider, PrivateRemote: raw.PrivateRemote}

	if entry.Block != nil {
		host.Block = raw.Blocks[entry.Block.Name]
	}

	// Only a provider that beehest deletes gets a default for idle_minutes; fillCounts replaces it with a written value.
	if entry.Idle {
		host.IdleMinutes = defaultIdleMins
	}

	return host
}

// checkBlocks refuses a block of another provider, idle_minutes and setup_command for a provider that beehest does not create the machine of, and credentials = "none" where the provider cannot keep it.
func checkBlocks(raw rawHost, provider model.HostProvider, entry *Entry) error {
	for _, name := range slices.Sorted(maps.Keys(raw.Blocks)) {
		if entry.Block != nil && name == entry.Block.Name {
			continue
		}

		return refuseAt(raw.At, "the `host` block holds the `%s` block, and `host.provider` %q does not take it. Remove the `%s` block, or set `host.provider` to the adapter that takes it.", name, provider, name)
	}

	if !entry.Idle && raw.IdleMinutes != idleUnset {
		return refuseAt(raw.At, "`host.idle_minutes` is not allowed for provider %q: beehest does not delete the machine. Remove the key.", provider)
	}

	if !entry.Idle && raw.SetupCommand.Set {
		return refuseAt(raw.At, "`host.setup_command` is not allowed for provider %q: beehest does not create the machine. Remove the key.", provider)
	}

	return checkCredentialsKey(raw, provider, entry)
}

// checkCredentialsKey refuses credentials = "none" for a provider whose own start tool can give the machine a credential of the owner. Any other value is refused later, for the value.
func checkCredentialsKey(raw rawHost, provider model.HostProvider, entry *Entry) error {
	if !entry.StartsWithOwnerCredentials || raw.Credentials.Value != string(model.HostCredentialsNone) {
		return nil
	}

	return refuseAt(raw.At, "`host.credentials` = \"none\" is not allowed for provider %q: the tool that starts the machine can forward your SSH agent and copies your cloud credentials to it, so beehest cannot keep that promise. Remove the key.", provider)
}

// fillKeys sets the keys of host that are not the provider name.
func fillKeys(raw rawHost, host *model.HostConfig) error {
	if raw.PrivateRemote != "" && !model.RemoteHasHost(raw.PrivateRemote) {
		return refuseAt(raw.At, "`host.private_remote` must name a machine, such as git@host:org/repo.git; a path or a file URL does not count. Fix the value.")
	}

	if raw.SetupCommand.Set && strings.TrimSpace(raw.SetupCommand.Value) == "" {
		return refuseAt(raw.At, "`host.setup_command` must not be empty. Give a command, such as \"make setup\", or remove the key.")
	}

	host.SetupCommand = raw.SetupCommand.Value

	if err := fillCounts(raw, host); err != nil {
		return err
	}

	if err := fillCredentials(raw, host); err != nil {
		return err
	}

	return fillDurations(raw, host)
}

// fillCredentials sets host.credentials. A host without the key keeps the empty value.
func fillCredentials(raw rawHost, host *model.HostConfig) error {
	if !raw.Credentials.Set {
		return nil
	}

	var err error

	host.Credentials, err = parseKey(raw.At, "host.credentials", raw.Credentials.Value, model.ParseHostCredentials)

	return err
}

// fillDurations sets the keys of host that are durations.
func fillDurations(raw rawHost, host *model.HostConfig) error {
	var err error

	if host.MaxRun, err = maxRun(raw.At, raw.MaxRun); err != nil {
		return err
	}

	if host.BankBudget, err = duration(raw.At, "host.bank_budget", raw.BankBudget, defaultBankBudget); err != nil {
		return err
	}

	if host.LostJobWait, err = lostJobWait(raw.At, raw.LostJobWait); err != nil {
		return err
	}

	return fillWaitLimits(raw, host)
}

// fillWaitLimits sets the two time limits of the wait for a job.
func fillWaitLimits(raw rawHost, host *model.HostConfig) error {
	var err error

	if host.LostAfter, err = duration(raw.At, "host.lost_after", raw.LostAfter, minutesText(DefaultLostAfter)); err != nil {
		return err
	}

	host.StopLimit, err = duration(raw.At, "host.stop_limit", raw.StopLimit, minutesText(DefaultStopLimit))

	return err
}

// maxRun parses the text of host.max_run and refuses a value under one second. Every consumer turns the limit into whole seconds, so a smaller
// value would turn the limit off. The refusal names the key and the smallest value.
func maxRun(at syntax.Position, text string) (time.Duration, error) {
	parsed, err := time.ParseDuration(text)
	if err != nil || parsed < minMaxRun {
		return 0, refuseAt(at, "`host.max_run` must be a duration of at least %s, such as \"%s\"; got `%s`. Fix the value.", minMaxRun, defaultMaxRun, text)
	}

	return parsed, nil
}

// lostJobWait parses the text of host.lost_job_wait and refuses a value outside the bounds. The refusal names the key and the range.
func lostJobWait(at syntax.Position, text string) (time.Duration, error) {
	parsed, err := time.ParseDuration(text)
	if err != nil || parsed < minLostJobWait || parsed > maxLostJobWait {
		return 0, refuseAt(at, "`host.lost_job_wait` must be a duration from 1m to 24h, such as \"%s\"; got `%s`. Fix the value.", defaultLostJobWaitText(), text)
	}

	return parsed, nil
}

// defaultLostJobWaitText is DefaultLostJobWait as a project file writes it, in minutes.
func defaultLostJobWaitText() string { return minutesText(DefaultLostJobWait) }

// minutesText writes a whole number of minutes as a project file does.
func minutesText(d time.Duration) string {
	return strconv.Itoa(int(d/time.Minute)) + "m"
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

	return fillLimits(raw, host)
}

// fillLimits sets the slot classes, the gate slots and the disk floor of host.
func fillLimits(raw rawHost, host *model.HostConfig) error {
	var err error

	if host.Slots, err = slotsOf(raw.At, raw.Slots); err != nil {
		return err
	}

	if host.GateSlots, err = positive(raw.At, "host.gate_slots", raw.GateSlots); err != nil {
		return err
	}

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
