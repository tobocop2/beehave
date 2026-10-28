package config

import (
	"errors"

	"github.com/hashicorp/hcl/v2"

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

// rawRunner is a runner block as written. Each key has a range for the refusals that come after the decode.
// A key that is not in the block has an empty range.
type rawRunner struct {
	At          hcl.Range `hcl:",def_range"`
	Name        string    `hcl:"name,optional"`
	NameAt      hcl.Range `hcl:"name,attr_name_range"`
	Agent       string    `hcl:"agent,optional"`
	AgentAt     hcl.Range `hcl:"agent,attr_name_range"`
	ConfigDir   *string   `hcl:"config_dir,optional"`
	ConfigDirAt hcl.Range `hcl:"config_dir,attr_name_range"`
	Keychain    *string   `hcl:"keychain,optional"`
	KeychainAt  hcl.Range `hcl:"keychain,attr_name_range"`
}

// at returns the range of a key, or the start of the block when the key is not in it.
func (r rawRunner) at(key hcl.Range) hcl.Range { return keyAt(key, r.At) }

type rawPool struct {
	FiveHourMax *float64 `hcl:"five_hour_max,optional"`
	SevenDayMax *float64 `hcl:"seven_day_max,optional"`
}

type rawAgents struct {
	MaxConcurrent *int `hcl:"max_concurrent,optional"`
}

type rawRunnersFile struct {
	Agents *rawAgents  `hcl:"agents,block"`
	Pool   *rawPool    `hcl:"pool,block"`
	Runner []rawRunner `hcl:"runner,block"`
}

func loadRunnersFile(path string) (agents, Pool, []model.Runner, error) {
	var raw rawRunnersFile
	if err := decodeFile(path, &raw); err != nil {
		return agents{}, Pool{}, nil, err
	}

	runners, err := buildRunners(raw.Runner)

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

func buildRunners(raws []rawRunner) ([]model.Runner, error) {
	seen := make(map[string]bool, len(raws))
	runners := make([]model.Runner, 0, len(raws))

	for _, raw := range raws {
		runner, err := buildRunner(raw)
		if err != nil {
			return nil, err
		}

		if seen[runner.Name] {
			return nil, refuseAt(raw.At, "runner `%s` appears twice. Give each runner its own name.", runner.Name)
		}

		seen[runner.Name] = true
		runners = append(runners, runner)
	}

	return runners, nil
}

func buildRunner(raw rawRunner) (model.Runner, error) {
	if err := errors.Join(requireText(raw.at(raw.NameAt), "name", raw.Name), requireText(raw.at(raw.AgentAt), "agent", raw.Agent)); err != nil {
		return model.Runner{}, err
	}

	agent, err := model.ParseAgent(raw.Agent)
	if err != nil {
		return model.Runner{}, refuseAt(raw.AgentAt, "runner `%s`: %w.", raw.Name, err)
	}

	configDir, err := optionalText(raw.ConfigDirAt, "config_dir", raw.ConfigDir, expandHome)
	if err != nil {
		return model.Runner{}, err
	}

	keychain, err := optionalText(raw.KeychainAt, "keychain", raw.Keychain, func(text string) (string, error) { return text, nil })

	return model.Runner{Name: raw.Name, Agent: agent, ConfigDir: configDir, Keychain: keychain}, err
}

// optionalText returns "" for an absent key, and the converted text for a present one.
func optionalText(at hcl.Range, key string, value *string, convert func(string) (string, error)) (string, error) {
	if value == nil {
		return "", nil
	}

	if err := requireText(at, key, *value); err != nil {
		return "", err
	}

	return convert(*value)
}
