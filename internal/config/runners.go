package config

import (
	"errors"
	"maps"
	"slices"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	maxPercent        = 100.0
	defaultConcurrent = 2
)

// Pool holds the usage caps of one agent for the runner pool, in percent, by the name of the usage window. A runner at or over a cap is not picked.
// A window with no cap is not limited.
type Pool map[string]float64

// declaredWindows returns the usage windows that the agents of the registry declare, once for each name, sorted by name.
// Two agents that declare one name must declare one default cap.
func declaredWindows(adapters Adapters) ([]model.WindowSpec, error) {
	owners := map[string]model.Agent{}
	seen := map[string]model.WindowSpec{}

	for _, agent := range slices.Sorted(maps.Keys(adapters.Agents)) {
		for _, window := range adapters.Agents[agent].Windows {
			if first, declared := seen[window.Name]; declared && first.DefaultMax != window.DefaultMax {
				return nil, refuse("the agents `%s` and `%s` declare the usage window `%s` with the default caps %g and %g. Declare one default for the name.",
					owners[window.Name], agent, window.Name, first.DefaultMax, window.DefaultMax)
			}

			if _, declared := seen[window.Name]; !declared {
				owners[window.Name], seen[window.Name] = agent, window
			}
		}
	}

	return slices.SortedFunc(maps.Values(seen), func(a, b model.WindowSpec) int { return strings.Compare(a.Name, b.Name) }), nil
}

// defaultPools returns the caps of each agent that declares a usage window, each at the default of its window.
func defaultPools(adapters Adapters) map[model.Agent]Pool {
	pools := map[model.Agent]Pool{}

	for agent, entry := range adapters.Agents {
		if len(entry.Windows) == 0 {
			continue
		}

		pool := Pool{}
		for _, window := range entry.Windows {
			pool[window.Name] = window.DefaultMax
		}

		pools[agent] = pool
	}

	return pools
}

// agents holds the limits on agent processes.
type agents struct {
	// MaxConcurrent is the global limit on agent processes. A laptop wants 2, a build host more.
	MaxConcurrent int
	// MaxReviewConcurrent is the limit on agent processes of the steps with edits = False. It is separate from MaxConcurrent: the two queues do not lend slots.
	MaxReviewConcurrent int
	// PerLogin is the most agent steps that one login runs at once, in each of the two agent queues. The default is 1.
	PerLogin int
	// Concurrency is the mode of the cap of the agents queue. The default is ConcurrencyStatic.
	Concurrency Concurrency
	// MaxTurns is the most agentic turns of one agent run. 0 means no cap.
	MaxTurns int
	// MaxBudgetUSD is the most dollars that one agent run spends on API calls. 0 means no cap.
	MaxBudgetUSD float64
	// ReviewPollMinutes is the minutes between two review polls. 0 means no poll.
	ReviewPollMinutes int
	// ReviewLookbackDays is the days that the review poll looks back for replies. The default is 3.
	ReviewLookbackDays int
	// MaxRunBudgetUSD is the most dollars that the agent steps of one item run spend in all, failed runs included. An agent step does not start when the sum is at or past it, so the last step can pass it by its own cost.
	// A child workflow that the run spawns starts its own sum. 0 means no cap.
	MaxRunBudgetUSD float64
	// MaxTreeBudgetUSD is the most dollars that the agent steps of an item tree spend in all: the root run and every run it spawns. An agent step does not start when the sum is at or past it.
	// A spawned run uses the cap of its root. 0 means no cap.
	MaxTreeBudgetUSD float64
	// MaxWeekBudgetUSD is the most dollars that the agent steps of all items spend in any seven days. An agent step does not start when the sum of the steps that ended in the last seven days is at or past it.
	// The sum reads the recorded runs of the store, so a `agents.retention_days` from 1 to 6 is refused with this cap. 0 means no cap.
	MaxWeekBudgetUSD float64
	// RetentionDays is the age in days at which `beehave run` deletes a finished run, with its recorded steps. The default is 7. 0 keeps every finished run.
	// A failed or canceled run is never deleted, because `beehave resume` can reopen it.
	RetentionDays int
	// TraceParent passes a W3C TRACEPARENT to each agent process, built from the run and the step. The default is false: no trace variable reaches an agent.
	TraceParent bool
	// ContextWindowTokens is the context window of the agent model in tokens. The max_context_percent of a step counts against it. The default is DefaultContextWindowTokens.
	// 0 means the window is unknown, and no context limit fires.
	ContextWindowTokens int
}

// runnersFile is the result of evaluating runners.star. A block that the file leaves out keeps its default.
type runnersFile struct {
	*loader

	agents   agents
	pools    map[model.Agent]Pool
	windows  []model.WindowSpec
	runners  []model.Runner
	adapters Adapters
}

// The schemas of the constructors of the runners file.
func agentsSpec() spec {
	return spec{"agents", []string{"max_concurrent?", "max_review_concurrent?", "max_turns?", "max_budget_usd?", "review_poll_minutes?", "max_run_budget_usd?", "retention_days?", "max_tree_budget_usd?", "per_login?", "review_lookback_days?", "concurrency?", "trace_parent?", "max_week_budget_usd?", "context_window_tokens?"}}
}

// poolSpec takes one optional argument for each usage window that an agent declares.
func poolSpec(windows []model.WindowSpec) spec {
	args := make([]string, len(windows))
	for i, window := range windows {
		args[i] = window.Name + "?"
	}

	return spec{"pool", args}
}

func runnerSpec() spec { return spec{"runner", []string{"name", "agent", "config_dir?", "keychain?"}} }

func runnersSpecs(windows []model.WindowSpec) []spec {
	return []spec{agentsSpec(), poolSpec(windows), runnerSpec()}
}

// loadRunnersFile evaluates runners.star. The pools hold the default caps of each agent; the pool call changes them.
func loadRunnersFile(path string, adapters Adapters, windows []model.WindowSpec, pools map[model.Agent]Pool) (agents, map[model.Agent]Pool, []model.Runner, error) {
	file := &runnersFile{loader: newLoader(path, runnersSpecs(windows)), agents: DefaultConfig().Agents, pools: pools, windows: windows, adapters: adapters}

	err := file.run(
		file.constructor(agentsSpec(), true, file.addAgents),
		file.constructor(poolSpec(windows), true, file.addPool),
		file.constructor(runnerSpec(), false, file.addRunner),
	)
	if err != nil {
		return agents{}, nil, nil, err
	}

	return file.agents, file.pools, file.runners, nil
}

func (f *runnersFile) addAgents(at syntax.Position, kwargs []starlark.Tuple) error {
	budget, runBudget, treeBudget, weekBudget := number(f.agents.MaxBudgetUSD), number(f.agents.MaxRunBudgetUSD), number(f.agents.MaxTreeBudgetUSD), number(f.agents.MaxWeekBudgetUSD)
	var mode starlark.Value = starlark.String(f.agents.Concurrency)

	if err := agentsSpec().unpack(at, kwargs, &f.agents.MaxConcurrent, &f.agents.MaxReviewConcurrent, &f.agents.MaxTurns, &budget, &f.agents.ReviewPollMinutes, &runBudget, &f.agents.RetentionDays, &treeBudget, &f.agents.PerLogin, &f.agents.ReviewLookbackDays, &mode, &f.agents.TraceParent, &weekBudget, &f.agents.ContextWindowTokens); err != nil {
		return err
	}

	text, isText := starlark.AsString(mode)
	if !isText {
		return refuseAt(at, "agents.concurrency is %s: set it to %q or %q. Set it in %s or in %s.", mode, ConcurrencyStatic, ConcurrencyAdaptive, f.path, envConcurrency)
	}

	f.agents.Concurrency = Concurrency(text)

	f.agents.MaxBudgetUSD, f.agents.MaxRunBudgetUSD, f.agents.MaxTreeBudgetUSD, f.agents.MaxWeekBudgetUSD = float64(budget), float64(runBudget), float64(treeBudget), float64(weekBudget)

	return nil
}

func (f *runnersFile) addPool(at syntax.Position, kwargs []starlark.Tuple) error {
	if err := f.refuseUnknownCaps(at, kwargs); err != nil {
		return err
	}

	caps := make([]number, len(f.windows))
	dests := make([]any, len(f.windows))

	for i := range f.windows {
		caps[i], dests[i] = number(f.windows[i].DefaultMax), &caps[i]
	}

	if err := poolSpec(f.windows).unpack(at, kwargs, dests...); err != nil {
		return err
	}

	for i, window := range f.windows {
		capWindow(f.pools, window.Name, float64(caps[i]))
	}

	return nil
}

// capWindow sets the cap of a usage window in the pool of every agent that declares it.
func capWindow(pools map[model.Agent]Pool, name string, percent float64) {
	for _, pool := range pools {
		if _, declared := pool[name]; declared {
			pool[name] = percent
		}
	}
}

// refuseUnknownCaps refuses a pool key that is not the name of a usage window. The refusal lists the names, and names the new form of a key that ends in _max.
func (f *runnersFile) refuseUnknownCaps(at syntax.Position, kwargs []starlark.Tuple) error {
	names := make([]string, len(f.windows))
	for i, window := range f.windows {
		names[i] = window.Name
	}

	for _, pair := range kwargs {
		key, _ := starlark.AsString(pair[0])

		if slices.Contains(names, key) {
			continue
		}

		if window, old := strings.CutSuffix(key, "_max"); old && slices.Contains(names, window) {
			return refuseAt(at, "`%s` is not a pool key. Write `%s = N` to cap the %s window.", key, window, window)
		}

		return refuseAt(at, "`%s` is not a usage window of any agent. Use one of: %s.", key, strings.Join(names, ", "))
	}

	return nil
}

func (f *runnersFile) addRunner(at syntax.Position, kwargs []starlark.Tuple) error {
	runner, err := f.readRunner(at, kwargs)
	if err != nil {
		return err
	}

	if slices.ContainsFunc(f.runners, func(seen model.Runner) bool { return seen.Name == runner.Name }) {
		return refuseAt(at, "runner `%s` appears twice. Give each runner its own name.", runner.Name)
	}

	f.runners = append(f.runners, runner)

	return nil
}

func (f *runnersFile) readRunner(at syntax.Position, kwargs []starlark.Tuple) (model.Runner, error) {
	var (
		name, agent         string
		configDir, keychain text
	)

	if err := runnerSpec().unpack(at, kwargs, &name, &agent, &configDir, &keychain); err != nil {
		return model.Runner{}, err
	}

	return f.buildRunner(at, name, agent, configDir, keychain)
}

func (f *runnersFile) buildRunner(at syntax.Position, name, agent string, configDir, keychain text) (model.Runner, error) {
	if err := errors.Join(requireText(at, "name", name), requireText(at, "agent", agent)); err != nil {
		return model.Runner{}, err
	}

	parsed, err := f.adapters.parseAgent(agent)
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
