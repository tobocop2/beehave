package config

import (
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/obee/internal/model"
)

const (
	defaultIdleMins  = 60
	defaultStaleMins = 20
	maxCount         = model.MaxDiskFloorGB
	defaultMaxRun    = "24h"
	hostTableMessage = "the `host` block %s. %s"
)

// rawHost is a host block as written. At is the position of its call.
type rawHost struct {
	At            syntax.Position
	Provider      string
	IdleMinutes   int
	StaleMinutes  int
	MaxRun        string
	DiskFloorGB   int
	DiskWaitMins  int
	PrivateRemote string
	SkyPilot      *rawSkyPilot
}

type rawSkyPilot struct {
	Cloud        string
	Region       string
	InstanceType string
}

// The schemas of the host and skypilot constructors.
func hostSpec() spec {
	return spec{"host", []string{"provider", "disk_floor_gb", "idle_minutes?", "stale_minutes?", "max_run?", "disk_wait_minutes?", "private_remote?", "skypilot?"}}
}

func skypilotSpec() spec { return spec{"skypilot", []string{"cloud?", "region?", "instance_type?"}} }

// newHost is the constructor of the host block. A key that the call leaves out keeps its default.
func newHost(at syntax.Position, kwargs []starlark.Tuple) (*rawHost, error) {
	raw := &rawHost{At: at, IdleMinutes: defaultIdleMins, StaleMinutes: defaultStaleMins, MaxRun: defaultMaxRun}

	skypilot := nested[rawSkyPilot]{want: skypilotSpec().name}

	err := hostSpec().unpack(at, kwargs, &raw.Provider, &raw.DiskFloorGB, &raw.IdleMinutes, &raw.StaleMinutes, &raw.MaxRun, &raw.DiskWaitMins, &raw.PrivateRemote, &skypilot)
	raw.SkyPilot = skypilot.block

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
func hostConfig(buildHost string, project rawProject) (*model.HostConfig, error) {
	raw := project.Host

	switch {
	case buildHost == LocalHost && raw == nil:
		return nil, nil //nolint:nilnil // a local build has no host table
	case buildHost == LocalHost:
		return nil, refuseAt(raw.At, hostTableMessage, "is not allowed when `build_host` is \"local\"", "Remove the block, or set `build_host` to a cluster name.")
	case raw == nil:
		return nil, refuseAt(project.At, hostTableMessage, "is required when `build_host` is a cluster name", "Add a `host` block with `provider` and `disk_floor_gb`.")
	}

	return buildHostConfig(*raw)
}

func buildHostConfig(raw rawHost) (*model.HostConfig, error) {
	provider, err := parseKey(raw.At, "host.provider", raw.Provider, model.ParseHostProvider)
	if err != nil {
		return nil, err
	}

	host := model.HostConfig{Provider: provider, PrivateRemote: raw.PrivateRemote}

	if raw.SkyPilot != nil {
		host.SkyPilot = model.SkyPilotConfig(*raw.SkyPilot)
	}

	if err = fillKeys(raw, &host); err != nil {
		return nil, err
	}

	return &host, nil
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

	host.MaxRun, err = maxRun(raw.At, raw.MaxRun)

	return err
}

// fillCounts sets the keys of host that are whole numbers.
func fillCounts(raw rawHost, host *model.HostConfig) error {
	var err error

	if host.IdleMinutes, err = positive(raw.At, "host.idle_minutes", raw.IdleMinutes); err != nil {
		return err
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

func maxRun(at syntax.Position, text string) (time.Duration, error) {
	parsed, err := time.ParseDuration(text)
	if err != nil || parsed <= 0 {
		return 0, refuseAt(at, "`host.max_run` must be a duration above zero, such as \"24h\"; got `%s`. Fix the value.", text)
	}

	return parsed, nil
}
