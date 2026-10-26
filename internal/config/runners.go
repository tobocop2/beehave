package config

import (
	"github.com/tobocop2/obee/internal/model"
)

const (
	maxPercent         = 100.0
	defaultFiveHourMax = 90.0
	defaultSevenDayMax = 95.0
	defaultConcurrent  = 2
)

// Pool holds the usage caps for the runner pool, in percent. A runner at or over a cap is not
// picked.
type Pool struct {
	FiveHourMax float64
	SevenDayMax float64
}

// agents holds the limits on agent processes.
type agents struct {
	// MaxConcurrent is the global limit on agent processes. A laptop wants 2, a build host more.
	MaxConcurrent int
}

type rawRunner struct {
	Name      string  `toml:"name"`
	Agent     string  `toml:"agent"`
	ConfigDir *string `toml:"config_dir"`
	Keychain  *string `toml:"keychain"`
}

type rawPool struct {
	FiveHourMax *float64 `toml:"five_hour_max"`
	SevenDayMax *float64 `toml:"seven_day_max"`
}

type rawAgents struct {
	MaxConcurrent *int `toml:"max_concurrent"`
}

type rawRunnersFile struct {
	Agents *rawAgents  `toml:"agents"`
	Pool   *rawPool    `toml:"pool"`
	Runner []rawRunner `toml:"runner"`
}

func loadRunnersFile(path string) (agents, Pool, []model.Runner, error) {
	var raw rawRunnersFile
	if err := decodeFile(path, &raw, runnersTop+"; [agents]: "+agentsKeys+"; [pool]: "+poolKeys+"; [[runner]]: "+runnerKeys); err != nil {
		return agents{}, Pool{}, nil, err
	}

	runners, err := buildRunners(path, raw.Runner)

	return buildAgents(raw.Agents), buildPool(raw.Pool), runners, err
}

func buildAgents(raw *rawAgents) agents {
	if raw != nil && raw.MaxConcurrent != nil {
		return agents{MaxConcurrent: *raw.MaxConcurrent}
	}

	return DefaultConfig().Agents
}

func buildPool(raw *rawPool) Pool {
	pool := DefaultConfig().Pool
	if raw == nil {
		return pool
	}

	if raw.FiveHourMax != nil {
		pool.FiveHourMax = *raw.FiveHourMax
	}

	if raw.SevenDayMax != nil {
		pool.SevenDayMax = *raw.SevenDayMax
	}

	return pool
}

func buildRunners(path string, raws []rawRunner) ([]model.Runner, error) {
	seen := make(map[string]bool, len(raws))
	runners := make([]model.Runner, 0, len(raws))

	for _, raw := range raws {
		runner, err := buildRunner(path, raw)
		if err != nil {
			return nil, err
		}

		if seen[runner.Name] {
			return nil, refuse("%s: runner `%s` appears twice. Give each runner its own name.", path, runner.Name)
		}

		seen[runner.Name] = true
		runners = append(runners, runner)
	}

	return runners, nil
}

func buildRunner(path string, raw rawRunner) (model.Runner, error) {
	for key, value := range map[string]string{"name": raw.Name, "agent": raw.Agent} {
		if err := requireText(path, key, value); err != nil {
			return model.Runner{}, err
		}
	}

	agent, err := model.ParseAgent(raw.Agent)
	if err != nil {
		return model.Runner{}, refuse("%s: runner `%s`: %w.", path, raw.Name, err)
	}

	configDir, err := optionalText(path, "config_dir", raw.ConfigDir, expandHome)
	if err != nil {
		return model.Runner{}, err
	}

	keychain, err := optionalText(path, "keychain", raw.Keychain, func(text string) (string, error) { return text, nil })

	return model.Runner{Name: raw.Name, Agent: agent, ConfigDir: configDir, Keychain: keychain}, err
}

// optionalText returns "" for an absent key, and the converted text for a present one.
func optionalText(path, key string, value *string, convert func(string) (string, error)) (string, error) {
	if value == nil {
		return "", nil
	}

	if err := requireText(path, key, *value); err != nil {
		return "", err
	}

	return convert(*value)
}
