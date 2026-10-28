package config

import (
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/tobocop2/obee/internal/model"
)

const (
	defaultIdleMins  = 60
	maxCount         = model.MaxDiskFloorGB
	defaultMaxRun    = 24 * time.Hour
	hostTableMessage = "the `host` block %s. %s"
)

// rawHost is a host block as written. Each key has a range for the refusals that come after the decode.
// A key that is not in the block has an empty range.
type rawHost struct {
	At              hcl.Range    `hcl:",def_range"`
	Provider        string       `hcl:"provider,optional"`
	ProviderAt      hcl.Range    `hcl:"provider,attr_name_range"`
	IdleMinutes     *int         `hcl:"idle_minutes,optional"`
	IdleMinutesAt   hcl.Range    `hcl:"idle_minutes,attr_name_range"`
	MaxRun          *string      `hcl:"max_run,optional"`
	MaxRunAt        hcl.Range    `hcl:"max_run,attr_name_range"`
	DiskFloorGB     *int         `hcl:"disk_floor_gb,optional"`
	DiskFloorGBAt   hcl.Range    `hcl:"disk_floor_gb,attr_name_range"`
	PrivateRemote   string       `hcl:"private_remote,optional"`
	PrivateRemoteAt hcl.Range    `hcl:"private_remote,attr_name_range"`
	SkyPilot        *rawSkyPilot `hcl:"skypilot,block"`
}

// at returns the range of a key, or the start of the block when the key is not in it.
func (h rawHost) at(key hcl.Range) hcl.Range { return keyAt(key, h.At) }

type rawSkyPilot struct {
	Cloud        string `hcl:"cloud,optional"`
	Region       string `hcl:"region,optional"`
	InstanceType string `hcl:"instance_type,optional"`
}

// hostConfig checks the host block against build_host and returns it. A project that builds locally has none.
// A block that must go points at itself. A block that is missing points at the build_host line.
func hostConfig(buildHost string, project rawProject) (*model.HostConfig, error) {
	raw := project.Host

	switch {
	case buildHost == LocalHost && raw == nil:
		return nil, nil //nolint:nilnil // a local build has no host table
	case buildHost == LocalHost:
		return nil, refuseAt(raw.At, hostTableMessage, "is not allowed when `build_host` is \"local\"", "Remove the block, or set `build_host` to a cluster name.")
	case raw == nil:
		return nil, refuseAt(project.BuildHostAt, hostTableMessage, "is required when `build_host` is a cluster name", "Add a `host` block with `provider` and `disk_floor_gb`.")
	}

	return buildHostConfig(*raw)
}

func buildHostConfig(raw rawHost) (*model.HostConfig, error) {
	if err := requireText(raw.at(raw.ProviderAt), "host.provider", raw.Provider); err != nil {
		return nil, err
	}

	provider, err := model.ParseHostProvider(raw.Provider)
	if err != nil {
		return nil, refuseAt(raw.ProviderAt, "`host.provider`: %w. Fix the value.", err)
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
		return refuseAt(raw.PrivateRemoteAt, "`host.private_remote` must name a machine, such as git@host:org/repo.git; a path or a file URL does not count. Fix the value.")
	}

	if host.IdleMinutes, err = positive(raw.at(raw.IdleMinutesAt), "host.idle_minutes", raw.IdleMinutes, defaultIdleMins); err != nil {
		return err
	}

	if host.DiskFloorGB, err = positive(raw.at(raw.DiskFloorGBAt), "host.disk_floor_gb", raw.DiskFloorGB, 0); err != nil {
		return err
	}

	host.MaxRun, err = maxRun(raw.MaxRunAt, raw.MaxRun)

	return err
}

// positive returns the value of an integer key. A missing key reads as fallback; a fallback of 0 makes the key required.
func positive(at hcl.Range, key string, value *int, fallback int) (int, error) {
	if value == nil && fallback > 0 {
		return fallback, nil
	}

	if value == nil {
		return 0, refuseAt(at, "`%s` is required. Add it.", key)
	}

	if *value <= 0 || *value > maxCount {
		return 0, refuseAt(at, "`%s` must be above zero and at most %d; got %d. Fix the value.", key, maxCount, *value)
	}

	return *value, nil
}

func maxRun(at hcl.Range, text *string) (time.Duration, error) {
	if text == nil {
		return defaultMaxRun, nil
	}

	parsed, err := time.ParseDuration(*text)
	if err != nil || parsed <= 0 {
		return 0, refuseAt(at, "`host.max_run` must be a duration above zero, such as \"24h\"; got `%s`. Fix the value.", *text)
	}

	return parsed, nil
}
