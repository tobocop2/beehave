// Package config loads the config folder and the state folder.
// It is the only package that reads the process environment.
package config

import (
	"errors"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/model"
)

// Names of the BEEHAVE_* variables that override the config files.
const (
	envMaxConcurrent       = "BEEHAVE_AGENTS_MAX_CONCURRENT"
	envMaxReviewConcurrent = "BEEHAVE_AGENTS_MAX_REVIEW_CONCURRENT"
	envPerLogin            = "BEEHAVE_AGENTS_PER_LOGIN"
	envConcurrency         = "BEEHAVE_AGENTS_CONCURRENCY"
	envMaxTurns            = "BEEHAVE_AGENTS_MAX_TURNS"
	envMaxBudget           = "BEEHAVE_AGENTS_MAX_BUDGET_USD"
	envMaxRunBudget        = "BEEHAVE_AGENTS_MAX_RUN_BUDGET_USD"
	envMaxTreeBudget       = "BEEHAVE_AGENTS_MAX_TREE_BUDGET_USD"
	envMaxWeekBudget       = "BEEHAVE_AGENTS_MAX_WEEK_BUDGET_USD"
	envTraceParent         = "BEEHAVE_AGENTS_TRACE_PARENT"
	envRetentionDays       = "BEEHAVE_AGENTS_RETENTION_DAYS"
	envReviewPoll          = "BEEHAVE_REVIEW_POLL_MINUTES"
	envReviewLookback      = "BEEHAVE_REVIEW_LOOKBACK_DAYS"
)

// envPoolMax returns the name of the variable that sets the cap of a usage window: BEEHAVE_POOL_<NAME>_MAX.
func envPoolMax(window string) string {
	return "BEEHAVE_POOL_" + strings.ToUpper(window) + "_MAX"
}

// maxReviewPollMinutes is one week. A larger value overflows the interval of the schedule.
const maxReviewPollMinutes = 10080

// DefaultContextWindowTokens is the default of agents.context_window_tokens: the context window of the agent model, in tokens. A model with a larger window needs the key set.
const DefaultContextWindowTokens = 200000

// DefaultReviewLookbackDays is the default of agents.review_lookback_days: the days that the review poll looks back for replies. The engine uses it when its options give none.
// maxReviewLookbackDays is one year.
const (
	DefaultReviewLookbackDays = 3
	maxReviewLookbackDays     = 365
)

// DefaultRetention is the age at which a finished run is deleted when no file or variable sets it. The engine uses it when its options give no age.
// maxRetentionDays is 100 years: a larger value overflows the duration.
const (
	DefaultRetention     = 7 * hoursPerDay * time.Hour
	hoursPerDay          = 24
	defaultRetentionDays = int(DefaultRetention / (hoursPerDay * time.Hour))
	maxRetentionDays     = 36500
)

// DefaultPerLogin is the default of agents.per_login: the agent steps that one login runs at once. The engine uses it when its options give none.
const DefaultPerLogin = 1

// floatBits is the bit size of a percent that the BEEHAVE_* variables give.
const floatBits = 64

// Config is the config folder: the agent limit, the pool caps of each agent, the runners and the projects.
type Config struct {
	Dir      string
	Agents   agents
	Pools    map[model.Agent]Pool
	Runners  []model.Runner
	Projects map[string]Project
}

// Problems lists what is wrong with a config, one entry for each problem.
type Problems []error

// Error joins the problems, one on each line. An empty list gives an empty text.
func (p Problems) Error() string {
	if joined := errors.Join(p...); joined != nil {
		return joined.Error()
	}

	return ""
}

// DefaultConfig returns the config of an empty folder.
func DefaultConfig() Config {
	return Config{
		Agents:   agents{MaxConcurrent: defaultConcurrent, MaxReviewConcurrent: defaultConcurrent, PerLogin: DefaultPerLogin, Concurrency: ConcurrencyStatic, ReviewLookbackDays: DefaultReviewLookbackDays, RetentionDays: defaultRetentionDays, ContextWindowTokens: DefaultContextWindowTokens},
		Pools:    map[model.Agent]Pool{},
		Projects: map[string]Project{},
	}
}

// LoadFolder loads runners.star and every projects/*.star of a config folder, applies the BEEHAVE_*
// variables of env, and validates the result against the adapters that the build registered. It reports every problem it finds, one for each file.
func LoadFolder(dir string, env map[string]string, adapters Adapters) (Config, Problems) {
	cfg := DefaultConfig()
	cfg.Dir = dir

	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return cfg, Problems{refuse("the config folder %s does not exist. Create it, or point --config or %s at a folder.", dir, ConfigDirVariable)}
	}

	problems := cfg.loadFiles(adapters)

	overlayConcurrency(env, &cfg.Agents.Concurrency)

	if err := cfg.applyEnv(env); err != nil {
		problems = append(problems, err)
	}

	if len(problems) == 0 {
		problems = cfg.Validate()
	}

	return cfg, problems
}

// loadFiles reads the runners file and the project files. Two agents that declare one usage window with two defaults refuse the registry, and no file is read.
func (c *Config) loadFiles(adapters Adapters) Problems {
	windows, err := declaredWindows(adapters)
	if err != nil {
		return Problems{err}
	}

	c.Pools = defaultPools(adapters)

	var problems Problems

	if err := c.loadRunners(adapters, windows); err != nil {
		problems = append(problems, err)
	}

	return append(problems, c.loadProjects(adapters)...)
}

func (c *Config) loadRunners(adapters Adapters, windows []model.WindowSpec) error {
	if old := filepath.Join(c.Dir, oldRunnersFileName); isFile(old) {
		return refuseOld(old, runnersFileName)
	}

	path := filepath.Join(c.Dir, runnersFileName)
	if !isFile(path) {
		return nil
	}

	var err error
	c.Agents, c.Pools, c.Runners, err = loadRunnersFile(path, adapters, windows, c.Pools)

	return err
}

// loadProjects reads projects/*.star in name order, and refuses a projects/*.hcl file. A missing projects folder holds no project.
func (c *Config) loadProjects(adapters Adapters) Problems {
	dir := filepath.Join(c.Dir, projectsDirName)

	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Problems{refuse("cannot read %s: %w. Check that you can read the folder.", dir, err)}
	}

	var problems Problems

	for _, entry := range entries {
		problems = c.addProject(problems, filepath.Join(dir, entry.Name()), adapters)
	}

	return problems
}

// addProject loads one file of the projects folder. A file of another kind is not a project.
func (c *Config) addProject(problems Problems, path string, adapters Adapters) Problems {
	switch filepath.Ext(path) {
	case oldProjectExt:
		return append(problems, refuseOld(path, strings.TrimSuffix(filepath.Base(path), oldProjectExt)+projectExt))
	case projectExt:
	default:
		return problems
	}

	project, err := loadProject(path, adapters)
	if err != nil {
		return append(problems, err)
	}

	c.Projects[project.Name] = project

	return problems
}

// overlayWhole reads a whole number from the environment. An empty variable leaves the value. The hint is the end of the error.
func overlayWhole(env map[string]string, name, hint string, into *int) error {
	value := env[name]
	if value == "" {
		return nil
	}

	number, err := strconv.Atoi(value)
	if err != nil {
		return refuse("%s=%s is not a whole number. Write digits only, %s", name, value, hint)
	}

	*into = number

	return nil
}

// overlayBool reads the text true or false from the environment. An empty variable leaves the value. Any other text is refused.
func overlayBool(env map[string]string, name string, into *bool) error {
	switch value := env[name]; value {
	case "":
	case "true":
		*into = true
	case "false":
		*into = false
	default:
		return refuse("%s=%s is not true or false. Write true or false.", name, value)
	}

	return nil
}

// overlayConcurrency sets the concurrency mode from the variable. An empty variable leaves the value; the check of the value is in Validate.
func overlayConcurrency(env map[string]string, into *Concurrency) {
	if value := env[envConcurrency]; value != "" {
		*into = Concurrency(value)
	}
}

func (c *Config) applyEnv(env map[string]string) error {
	err := errors.Join(
		overlayWhole(env, envMaxConcurrent, "for example 2.", &c.Agents.MaxConcurrent),
		overlayWhole(env, envMaxReviewConcurrent, "for example 2.", &c.Agents.MaxReviewConcurrent),
		overlayWhole(env, envPerLogin, "for example 2.", &c.Agents.PerLogin),
		overlayWhole(env, envMaxTurns, "for example 40, or 0 for no cap.", &c.Agents.MaxTurns),
		overlayWhole(env, envReviewPoll, "for example 60, or 0 for no poll.", &c.Agents.ReviewPollMinutes),
		overlayWhole(env, envReviewLookback, "for example 3.", &c.Agents.ReviewLookbackDays),
		overlayWhole(env, envRetentionDays, "for example 30, or 0 to keep every finished run.", &c.Agents.RetentionDays),
		c.overlayBudgets(env),
	)
	if err != nil {
		return err
	}

	return errors.Join(
		c.overlayPools(env),
		overlayBool(env, envTraceParent, &c.Agents.TraceParent),
	)
}

// windowCaps returns the cap of each usage window that the pools hold. Agents that share a window name share its cap.
func (c Config) windowCaps() map[string]float64 {
	caps := map[string]float64{}

	for _, pool := range c.Pools {
		maps.Copy(caps, pool)
	}

	return caps
}

// overlayPools reads the cap of each usage window from its variable. The cap applies to every agent that declares the window.
func (c *Config) overlayPools(env map[string]string) error {
	var problems []error

	caps := c.windowCaps()

	for _, name := range slices.Sorted(maps.Keys(caps)) {
		percent := caps[name]

		if err := overlayPercent(env, envPoolMax(name), &percent); err != nil {
			problems = append(problems, err)

			continue
		}

		capWindow(c.Pools, name, percent)
	}

	return errors.Join(problems...)
}

// overlayBudgets reads the caps in dollars from the environment.
func (c *Config) overlayBudgets(env map[string]string) error {
	return errors.Join(
		overlayDollars(env, envMaxBudget, "5", &c.Agents.MaxBudgetUSD),
		overlayDollars(env, envMaxRunBudget, "20", &c.Agents.MaxRunBudgetUSD),
		overlayDollars(env, envMaxTreeBudget, "100", &c.Agents.MaxTreeBudgetUSD),
		overlayDollars(env, envMaxWeekBudget, "300", &c.Agents.MaxWeekBudgetUSD),
	)
}

// overlayDollars reads a cap in dollars from the environment into into. The example is the number that the error shows.
func overlayDollars(env map[string]string, name, example string, into *float64) error {
	value := env[name]
	if value == "" {
		return nil
	}

	dollars, err := strconv.ParseFloat(value, floatBits)
	if err != nil || math.IsInf(dollars, 0) || math.IsNaN(dollars) {
		return refuse("%s=%s is not a number. Write dollars, for example %s, or 0 for no cap.", name, value, example)
	}

	*into = dollars

	return nil
}

func overlayPercent(env map[string]string, name string, target *float64) error {
	value := env[name]
	if value == "" {
		return nil
	}

	percent, err := strconv.ParseFloat(value, floatBits)
	if err != nil {
		return refuse("%s=%s is not a number. Write a number above 0 and up to 100.", name, value)
	}

	*target = percent

	return nil
}

// limitProblems checks the two limits on agent processes and the mode of the cap of the agents queue.
func (c Config) limitProblems(runnersPath string) Problems {
	problems := c.concurrencyProblems(runnersPath)

	if c.Agents.MaxConcurrent < 1 {
		problems = append(problems, refuse("agents.max_concurrent must be 1 or more. Set it in %s or in %s.", runnersPath, envMaxConcurrent))
	}

	if c.Agents.MaxReviewConcurrent < 1 {
		problems = append(problems, refuse("agents.max_review_concurrent must be 1 or more. Set it in %s or in %s.", runnersPath, envMaxReviewConcurrent))
	}

	if c.Agents.ContextWindowTokens < 0 {
		problems = append(problems, refuse("agents.context_window_tokens must be 0 (unknown, so no context limit fires) or more. Set it in %s.", runnersPath))
	}

	return problems
}

// perLoginProblems refuses a per_login below 1, and one above the cap of an agent queue: a queue cannot run more agents for one login than it runs in all.
func (c Config) perLoginProblems(runnersPath string) Problems {
	perLogin := c.Agents.PerLogin

	switch {
	case perLogin < 1:
		return Problems{refuse("agents.per_login must be 1 or more. Set it in %s or in %s.", runnersPath, envPerLogin)}
	case min(c.Agents.MaxConcurrent, c.Agents.MaxReviewConcurrent) >= 1 && perLogin > min(c.Agents.MaxConcurrent, c.Agents.MaxReviewConcurrent):
		return Problems{refuse("agents.per_login is %d: set it to agents.max_concurrent (%d) or agents.max_review_concurrent (%d), whichever is lower, or less. Set it in %s or in %s.", perLogin, c.Agents.MaxConcurrent, c.Agents.MaxReviewConcurrent, runnersPath, envPerLogin)}
	}

	return nil
}

// concurrencyProblems refuses a concurrency mode other than the two that the engine knows.
func (c Config) concurrencyProblems(runnersPath string) Problems {
	if c.Agents.Concurrency.Known() {
		return nil
	}

	return Problems{refuse("agents.concurrency is %q: set it to %q or %q. Set it in %s or in %s.", string(c.Agents.Concurrency), ConcurrencyStatic, ConcurrencyAdaptive, runnersPath, envConcurrency)}
}

// Validate checks the values that no single file owns: the agent limit and the pool caps.
func (c Config) Validate() Problems {
	var problems Problems

	runnersPath := filepath.Join(c.Dir, runnersFileName)

	problems = append(problems, c.limitProblems(runnersPath)...)

	problems = append(problems, c.perLoginProblems(runnersPath)...)

	if c.Agents.MaxTurns < 0 {
		problems = append(problems, refuse("agents.max_turns must be 0 (no cap) or more. Set it in %s or in %s.", runnersPath, envMaxTurns))
	}

	problems = append(problems, c.budgetProblems(runnersPath)...)
	problems = append(problems, c.pollProblems(runnersPath)...)
	problems = append(problems, c.lookbackProblems(runnersPath)...)
	problems = append(problems, c.retentionProblems(runnersPath)...)
	problems = append(problems, c.slotProblems()...)

	return append(problems, c.poolProblems(runnersPath)...)
}

// budgetProblems refuses a cap in dollars that is negative, infinite or not a number. The negated form also refuses NaN, which fails every comparison.
func (c Config) budgetProblems(runnersPath string) Problems {
	var problems Problems

	for _, limit := range []struct {
		name, env string
		dollars   float64
	}{
		{"agents.max_budget_usd", envMaxBudget, c.Agents.MaxBudgetUSD},
		{"agents.max_run_budget_usd", envMaxRunBudget, c.Agents.MaxRunBudgetUSD},
		{"agents.max_tree_budget_usd", envMaxTreeBudget, c.Agents.MaxTreeBudgetUSD},
		{"agents.max_week_budget_usd", envMaxWeekBudget, c.Agents.MaxWeekBudgetUSD},
	} {
		if !(limit.dollars >= 0) || math.IsInf(limit.dollars, 0) {
			problems = append(problems, refuse("%s must be 0 (no cap) or more, and a finite number. Set it in %s or in %s.", limit.name, runnersPath, limit.env))
		}
	}

	return problems
}

func (c Config) pollProblems(runnersPath string) Problems {
	switch minutes := c.Agents.ReviewPollMinutes; {
	case minutes < 0:
		return Problems{refuse("agents.review_poll_minutes must be 0 (no poll) or more. Set it in %s or in %s.", runnersPath, envReviewPoll)}
	case minutes > maxReviewPollMinutes:
		return Problems{refuse("agents.review_poll_minutes is %d: set it to %d (one week) or less. Set it in %s or in %s.", minutes, maxReviewPollMinutes, runnersPath, envReviewPoll)}
	}

	return nil
}

func (c Config) lookbackProblems(runnersPath string) Problems {
	switch days := c.Agents.ReviewLookbackDays; {
	case days < 1:
		return Problems{refuse("agents.review_lookback_days must be 1 or more. Set it in %s or in %s.", runnersPath, envReviewLookback)}
	case days > maxReviewLookbackDays:
		return Problems{refuse("agents.review_lookback_days is %d: set it to %d (one year) or less. Set it in %s or in %s.", days, maxReviewLookbackDays, runnersPath, envReviewLookback)}
	}

	return nil
}

func (c Config) retentionProblems(runnersPath string) Problems {
	switch days := c.Agents.RetentionDays; {
	case days < 0:
		return Problems{refuse("agents.retention_days must be 0 (keep for ever) or more. Set it in %s or in %s.", runnersPath, envRetentionDays)}
	case days > maxRetentionDays:
		return Problems{refuse("agents.retention_days is %d: set it to %d (100 years) or less. Set it in %s or in %s.", days, maxRetentionDays, runnersPath, envRetentionDays)}
	}

	return nil
}

func (c Config) poolProblems(runnersPath string) Problems {
	var problems Problems

	caps := c.windowCaps()

	for _, name := range slices.Sorted(maps.Keys(caps)) {
		// The negated form also refuses NaN, which fails every comparison.
		if !(caps[name] > 0 && caps[name] <= maxPercent) {
			problems = append(problems, refuse("`%s` in the pool block must be a number above 0 and up to 100. Set it in %s or in %s.",
				name, runnersPath, envPoolMax(name)))
		}
	}

	return problems
}
