package config

import (
	"time"

	"github.com/tobocop2/obee/internal/model"
)

const (
	hostKeys         = "provider, idle_minutes, max_run, disk_floor_gb, private_remote; [host.skypilot]: cloud, region, instance_type"
	defaultIdleMins  = 60
	maxCount         = model.MaxDiskFloorGB
	defaultMaxRun    = 24 * time.Hour
	hostTableMessage = "%s: the `[host]` table %s. %s"
)

type rawHost struct {
	Provider      string       `toml:"provider"`
	IdleMinutes   *int         `toml:"idle_minutes"`
	MaxRun        *string      `toml:"max_run"`
	DiskFloorGB   *int         `toml:"disk_floor_gb"`
	PrivateRemote string       `toml:"private_remote"`
	SkyPilot      *rawSkyPilot `toml:"skypilot"`
}

type rawSkyPilot struct {
	Cloud        string `toml:"cloud"`
	Region       string `toml:"region"`
	InstanceType string `toml:"instance_type"`
}

// hostConfig checks the [host] table against build_host and returns it. A project that builds locally has none.
func hostConfig(path, buildHost string, raw *rawHost) (*model.HostConfig, error) {
	switch {
	case buildHost == LocalHost && raw == nil:
		return nil, nil //nolint:nilnil // a local build has no host table
	case buildHost == LocalHost:
		return nil, refuse(hostTableMessage, path, "is not allowed when `build_host` is \"local\"", "Remove the table, or set `build_host` to a cluster name.")
	case raw == nil:
		return nil, refuse(hostTableMessage, path, "is required when `build_host` is a cluster name", "Add `[host]` with `provider` and `disk_floor_gb`.")
	}

	return buildHostConfig(path, *raw)
}

func buildHostConfig(path string, raw rawHost) (*model.HostConfig, error) {
	if err := requireText(path, "host.provider", raw.Provider); err != nil {
		return nil, err
	}

	provider, err := model.ParseHostProvider(raw.Provider)
	if err != nil {
		return nil, refuse("%s: `host.provider`: %w. Fix the value.", path, err)
	}

	host := model.HostConfig{Provider: provider, PrivateRemote: raw.PrivateRemote}

	if raw.SkyPilot != nil {
		host.SkyPilot = model.SkyPilotConfig(*raw.SkyPilot)
	}

	if err = fillKeys(path, raw, &host); err != nil {
		return nil, err
	}

	return &host, nil
}

// fillKeys sets the keys of host that are not the provider name.
func fillKeys(path string, raw rawHost, host *model.HostConfig) error {
	var err error

	if raw.PrivateRemote != "" && !model.RemoteHasHost(raw.PrivateRemote) {
		return refuse("%s: `host.private_remote` must name a machine, such as git@host:org/repo.git; a path or a file URL does not count. Fix the value.", path)
	}

	if host.IdleMinutes, err = positive(path, "host.idle_minutes", raw.IdleMinutes, defaultIdleMins); err != nil {
		return err
	}

	if host.DiskFloorGB, err = positive(path, "host.disk_floor_gb", raw.DiskFloorGB, 0); err != nil {
		return err
	}

	host.MaxRun, err = maxRun(path, raw.MaxRun)

	return err
}

// positive returns the value of an integer key. A missing key reads as fallback; a fallback of 0 makes the key required.
func positive(path, key string, value *int, fallback int) (int, error) {
	if value == nil && fallback > 0 {
		return fallback, nil
	}

	if value == nil {
		return 0, refuse("%s: `%s` is required. Add it.", path, key)
	}

	if *value <= 0 || *value > maxCount {
		return 0, refuse("%s: `%s` must be above zero and at most %d; got %d. Fix the value.", path, key, maxCount, *value)
	}

	return *value, nil
}

func maxRun(path string, text *string) (time.Duration, error) {
	if text == nil {
		return defaultMaxRun, nil
	}

	parsed, err := time.ParseDuration(*text)
	if err != nil || parsed <= 0 {
		return 0, refuse("%s: `host.max_run` must be a duration above zero, such as \"24h\"; got `%s`. Fix the value.", path, *text)
	}

	return parsed, nil
}
