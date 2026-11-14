package config

import (
	"regexp"
	"strings"
	"time"
	"unicode"

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
)

// rawHost is a host block as written. At is the position of its call.
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
	SkyPilot      *rawSkyPilot
	SSH           *rawSSH
}

type rawSkyPilot struct {
	Cloud        string
	Region       string
	InstanceType string
}

type rawSSH struct {
	At         syntax.Position
	Address    string
	User       string
	Port       int
	Identity   string
	WorkRoot   string
	KnownHosts string
}

// The schemas of the host, skypilot and ssh constructors.
func hostSpec() spec {
	return spec{"host", []string{"provider", "disk_floor_gb", "idle_minutes?", "stale_minutes?", "max_run?", "disk_wait_minutes?", "bank_budget?", "private_remote?", "skypilot?", "ssh?"}}
}

func skypilotSpec() spec { return spec{"skypilot", []string{"cloud?", "region?", "instance_type?"}} }

func sshSpec() spec {
	return spec{"ssh", []string{"address?", "user?", "port?", "identity?", "work_root?", "known_hosts?"}}
}

// idleUnset and portUnset are the values of a key that the call leaves out, so that the loader can tell an omitted key from a written one.
const (
	idleUnset = -1 << 31
	portUnset = -1
)

// newHost is the constructor of the host block. A key that the call leaves out keeps its default.
func newHost(at syntax.Position, kwargs []starlark.Tuple) (*rawHost, error) {
	raw := &rawHost{At: at, IdleMinutes: idleUnset, StaleMinutes: defaultStaleMins, MaxRun: defaultMaxRun, BankBudget: defaultBankBudget}

	skypilot := nested[rawSkyPilot]{want: skypilotSpec().name}
	ssh := nested[rawSSH]{want: sshSpec().name}

	err := hostSpec().unpack(at, kwargs, &raw.Provider, &raw.DiskFloorGB, &raw.IdleMinutes, &raw.StaleMinutes, &raw.MaxRun, &raw.DiskWaitMins, &raw.BankBudget, &raw.PrivateRemote, &skypilot, &ssh)
	raw.SkyPilot, raw.SSH = skypilot.block, ssh.block

	return raw, err
}

// newSSH is the constructor of the ssh block.
func newSSH(at syntax.Position, kwargs []starlark.Tuple) (*rawSSH, error) {
	raw := &rawSSH{At: at, Port: portUnset, KnownHosts: model.KnownHostsStrict}
	err := sshSpec().unpack(at, kwargs, &raw.Address, &raw.User, &raw.Port, &raw.Identity, &raw.WorkRoot, &raw.KnownHosts)

	return raw, err
}

// newSkyPilot is the constructor of the skypilot block.
func newSkyPilot(at syntax.Position, kwargs []starlark.Tuple) (*rawSkyPilot, error) {
	raw := &rawSkyPilot{}
	err := skypilotSpec().unpack(at, kwargs, &raw.Cloud, &raw.Region, &raw.InstanceType)

	return raw, err
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

	host := model.HostConfig{Provider: provider, PrivateRemote: raw.PrivateRemote}

	if err = fillHost(raw, &host, *registry.Hosts[provider]); err != nil {
		return nil, err
	}

	return &host, nil
}

// fillHost checks the blocks of the host block against the entry of its provider, then sets the block of the provider and the keys.
func fillHost(raw rawHost, host *model.HostConfig, entry Entry) error {
	if err := checkBlocks(raw, host.Provider, entry); err != nil {
		return err
	}

	if err := fillProvider(raw, host, entry); err != nil {
		return err
	}

	return fillKeys(raw, host)
}

// fillProvider sets the block of the provider. Only a provider that beehave deletes after idle_minutes gets a default for that key; fillCounts replaces it with a written value.
func fillProvider(raw rawHost, host *model.HostConfig, entry Entry) error {
	if entry.HostBlock == skypilotSpec().name {
		if raw.SkyPilot != nil {
			host.SkyPilot = model.SkyPilotConfig(*raw.SkyPilot)
		}
	} else {
		var err error

		host.SSH, err = sshConfig(raw.SSH)
		if err != nil {
			return err
		}
	}

	if entry.Idle {
		host.IdleMinutes = defaultIdleMins
	}

	return nil
}

// checkBlocks refuses a block of another provider, and idle_minutes for a provider that beehave does not delete the machine of.
func checkBlocks(raw rawHost, provider model.HostProvider, entry Entry) error {
	want := entry.HostBlock

	for _, block := range []struct {
		name    string
		written bool
	}{{skypilotSpec().name, raw.SkyPilot != nil}, {sshSpec().name, raw.SSH != nil}} {
		if block.written && block.name != want {
			return refuseAt(raw.At, "`host.provider` %q takes the `%s` block, not the `%s` block. Remove the `%s` block.", provider, want, block.name, block.name)
		}
	}

	if !entry.Idle && raw.IdleMinutes != idleUnset {
		return refuseAt(raw.At, "`host.idle_minutes` is not allowed for provider %q: beehave does not delete the machine. Remove the key.", provider)
	}

	return nil
}

// hostWord is the pattern of a name that can follow ssh options: it cannot start an option or hold a space.
var hostWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]*$`)

// sshConfig checks the ssh block. A project with no block gets the defaults.
func sshConfig(raw *rawSSH) (model.SSHConfig, error) {
	if raw == nil {
		return model.SSHConfig{KnownHosts: model.KnownHostsStrict}, nil
	}

	if err := checkSSHText(*raw); err != nil {
		return model.SSHConfig{}, err
	}

	port, err := sshPort(*raw)
	if err != nil {
		return model.SSHConfig{}, err
	}

	return model.SSHConfig{Address: raw.Address, User: raw.User, Port: port, Identity: raw.Identity, WorkRoot: raw.WorkRoot, KnownHosts: raw.KnownHosts}, nil
}

// sshPort returns the port, or zero when the call leaves it to the ssh config.
func sshPort(raw rawSSH) (int, error) {
	switch {
	case raw.Port == portUnset:
		return 0, nil
	case raw.Port < 1 || raw.Port > maxPort:
		return 0, refuseAt(raw.At, "`host.ssh.port` must be from 1 to %d; got %d. Fix the value.", maxPort, raw.Port)
	}

	return raw.Port, nil
}

const maxPort = 65535

// checkSSHText checks the name and path keys of the ssh block.
func checkSSHText(raw rawSSH) error {
	if raw.KnownHosts != model.KnownHostsStrict && raw.KnownHosts != model.KnownHostsAcceptNew {
		return refuseAt(raw.At, "`host.ssh.known_hosts` must be %s or %s. A host key that changes is always refused.", model.KnownHostsStrict, model.KnownHostsAcceptNew)
	}

	for _, check := range []error{
		checkWord(raw.At, "address", raw.Address), checkWord(raw.At, "user", raw.User),
		checkPath(raw.At, "identity", raw.Identity), checkPath(raw.At, "work_root", raw.WorkRoot),
	} {
		if check != nil {
			return check
		}
	}

	return nil
}

func checkWord(at syntax.Position, key, value string) error {
	if value != "" && !hostWord.MatchString(value) {
		return refuseAt(at, "`host.ssh.%s` must start with a letter or digit and hold only letters, digits, dots, underscores, at signs and dashes; got `%s`. Fix the value.", key, value)
	}

	return nil
}

// checkPath checks a path key of the ssh block: empty, or absolute (or under ~/), with no control character.
func checkPath(at syntax.Position, key, value string) error {
	switch {
	case value == "":
		return nil
	case strings.ContainsFunc(value, unicode.IsControl):
		return refuseAt(at, "`host.ssh.%s` must hold no control character. Fix the value.", key)
	case !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "~/"):
		return refuseAt(at, "`host.ssh.%s` must be an absolute path or start with ~/; got `%s`. Fix the value.", key, value)
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
