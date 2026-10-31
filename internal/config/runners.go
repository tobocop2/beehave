package config

import (
	"errors"
	"slices"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

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
	// MaxReviewConcurrent is the limit on agent processes of the steps of the kind review. It is separate from MaxConcurrent: the two queues do not lend slots.
	MaxReviewConcurrent int
	// MaxTurns is the most agentic turns of one agent run. 0 means no cap.
	MaxTurns int
	// MaxBudgetUSD is the most dollars that one agent run spends on API calls. 0 means no cap.
	MaxBudgetUSD float64
	// ReviewPollMinutes is the minutes between two review polls. 0 means no poll.
	ReviewPollMinutes int
}

// runnersFile is the result of evaluating runners.star. A block that the file leaves out keeps its default.
type runnersFile struct {
	*loader

	agents  agents
	pool    Pool
	runners []model.Runner
}

// The schemas of the constructors of the runners file.
func agentsSpec() spec {
	return spec{"agents", []string{"max_concurrent?", "max_review_concurrent?", "max_turns?", "max_budget_usd?", "review_poll_minutes?"}}
}

func poolSpec() spec { return spec{"pool", []string{"five_hour_max?", "seven_day_max?"}} }

func runnerSpec() spec { return spec{"runner", []string{"name", "agent", "config_dir?", "keychain?"}} }

func runnersSpecs() []spec { return []spec{agentsSpec(), poolSpec(), runnerSpec()} }

func loadRunnersFile(path string) (agents, Pool, []model.Runner, error) {
	file := &runnersFile{loader: newLoader(path, runnersSpecs()), agents: DefaultConfig().Agents, pool: DefaultConfig().Pool}

	err := file.run(
		file.constructor(agentsSpec(), true, file.addAgents),
		file.constructor(poolSpec(), true, file.addPool),
		file.constructor(runnerSpec(), false, file.addRunner),
	)
	if err != nil {
		return agents{}, Pool{}, nil, err
	}

	return file.agents, file.pool, file.runners, nil
}

func (f *runnersFile) addAgents(at syntax.Position, kwargs []starlark.Tuple) error {
	budget := number(f.agents.MaxBudgetUSD)

	if err := agentsSpec().unpack(at, kwargs, &f.agents.MaxConcurrent, &f.agents.MaxReviewConcurrent, &f.agents.MaxTurns, &budget, &f.agents.ReviewPollMinutes); err != nil {
		return err
	}

	f.agents.MaxBudgetUSD = float64(budget)

	return nil
}

func (f *runnersFile) addPool(at syntax.Position, kwargs []starlark.Tuple) error {
	five, seven := number(f.pool.FiveHourMax), number(f.pool.SevenDayMax)

	if err := poolSpec().unpack(at, kwargs, &five, &seven); err != nil {
		return err
	}

	f.pool.FiveHourMax, f.pool.SevenDayMax = float64(five), float64(seven)

	return nil
}

func (f *runnersFile) addRunner(at syntax.Position, kwargs []starlark.Tuple) error {
	runner, err := readRunner(at, kwargs)
	if err != nil {
		return err
	}

	if slices.ContainsFunc(f.runners, func(seen model.Runner) bool { return seen.Name == runner.Name }) {
		return refuseAt(at, "runner `%s` appears twice. Give each runner its own name.", runner.Name)
	}

	f.runners = append(f.runners, runner)

	return nil
}

func readRunner(at syntax.Position, kwargs []starlark.Tuple) (model.Runner, error) {
	var (
		name, agent         string
		configDir, keychain text
	)

	if err := runnerSpec().unpack(at, kwargs, &name, &agent, &configDir, &keychain); err != nil {
		return model.Runner{}, err
	}

	return buildRunner(at, name, agent, configDir, keychain)
}

func buildRunner(at syntax.Position, name, agent string, configDir, keychain text) (model.Runner, error) {
	if err := errors.Join(requireText(at, "name", name), requireText(at, "agent", agent)); err != nil {
		return model.Runner{}, err
	}

	parsed, err := model.ParseAgent(agent)
	if err != nil {
		return model.Runner{}, refuseAt(at, "runner `%s`: %w.", name, err)
	}

	dir, err := optionalText(at, "config_dir", configDir, expandHome)
	if err != nil {
		return model.Runner{}, err
	}

	login, err := optionalText(at, "keychain", keychain, func(value string) (string, error) { return value, nil })

	return model.Runner{Name: name, Agent: parsed, ConfigDir: dir, Keychain: login}, err
}

// optionalText returns "" for an absent key, and the converted text for a present one.
func optionalText(at syntax.Position, key string, value text, convert func(string) (string, error)) (string, error) {
	if !value.Set {
		return "", nil
	}

	if err := requireText(at, key, value.Value); err != nil {
		return "", err
	}

	return convert(value.Value)
}
