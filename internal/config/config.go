// Package config loads the config folder and the state folder.
// It is the only package that reads the process environment.
package config

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// Names of the BEEHAVE_* variables that override the config files.
const (
	envMaxConcurrent       = "BEEHAVE_AGENTS_MAX_CONCURRENT"
	envMaxReviewConcurrent = "BEEHAVE_AGENTS_MAX_REVIEW_CONCURRENT"
	envMaxTurns            = "BEEHAVE_AGENTS_MAX_TURNS"
	envMaxBudget           = "BEEHAVE_AGENTS_MAX_BUDGET_USD"
	envMaxRunBudget        = "BEEHAVE_AGENTS_MAX_RUN_BUDGET_USD"
	envRetentionDays       = "BEEHAVE_AGENTS_RETENTION_DAYS"
	envReviewPoll          = "BEEHAVE_REVIEW_POLL_MINUTES"
	envFiveHourMax         = "BEEHAVE_POOL_FIVE_HOUR_MAX"
	envSevenDayMax         = "BEEHAVE_POOL_SEVEN_DAY_MAX"
)

// maxReviewPollMinutes is one week. A larger value overflows the interval of the schedule.
const maxReviewPollMinutes = 10080

// defaultRetentionDays is the age at which a finished run is deleted when no file or variable sets it. maxRetentionDays is 100 years: a larger value overflows the duration.
const (
	defaultRetentionDays = 7
	maxRetentionDays     = 36500
)

// floatBits is the bit size of a percent that the BEEHAVE_* variables give.
const floatBits = 64

// Config is the config folder: the agent limit, the pool caps, the runners and the projects.
type Config struct {
	Dir      string
	Agents   agents
	Pool     Pool
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
		Agents:   agents{MaxConcurrent: defaultConcurrent, MaxReviewConcurrent: defaultConcurrent, RetentionDays: defaultRetentionDays},
		Pool:     Pool{FiveHourMax: defaultFiveHourMax, SevenDayMax: defaultSevenDayMax},
		Projects: map[string]Project{},
	}
}

// LoadFolder loads runners.star and every projects/*.star of a config folder, applies the BEEHAVE_*
// variables of env, and validates the result. It reports every problem it finds, one for each file.
func LoadFolder(dir string, env map[string]string) (Config, Problems) {
	cfg := DefaultConfig()
	cfg.Dir = dir

	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return cfg, Problems{refuse("the config folder %s does not exist. Create it, or point --config or %s at a folder.", dir, ConfigDirVariable)}
	}

	problems := cfg.loadFiles()
	if err := cfg.applyEnv(env); err != nil {
		problems = append(problems, err)
	}

	if len(problems) == 0 {
		problems = cfg.Validate()
	}

	return cfg, problems
}

func (c *Config) loadFiles() Problems {
	var problems Problems

	if err := c.loadRunners(); err != nil {
		problems = append(problems, err)
	}

	return append(problems, c.loadProjects()...)
}

func (c *Config) loadRunners() error {
	if old := filepath.Join(c.Dir, oldRunnersFileName); isFile(old) {
		return refuseOld(old, runnersFileName)
	}

	path := filepath.Join(c.Dir, runnersFileName)
	if !isFile(path) {
		return nil
	}

	var err error
	c.Agents, c.Pool, c.Runners, err = loadRunnersFile(path)

	return err
}

// loadProjects reads projects/*.star in name order, and refuses a projects/*.hcl file. A missing projects folder holds no project.
func (c *Config) loadProjects() Problems {
	dir := filepath.Join(c.Dir, projectsDirName)

	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Problems{refuse("cannot read %s: %w. Check that you can read the folder.", dir, err)}
	}

	var problems Problems

	for _, entry := range entries {
		problems = c.addProject(problems, filepath.Join(dir, entry.Name()))
	}

	return problems
}

// addProject loads one file of the projects folder. A file of another kind is not a project.
func (c *Config) addProject(problems Problems, path string) Problems {
	switch filepath.Ext(path) {
	case oldProjectExt:
		return append(problems, refuseOld(path, strings.TrimSuffix(filepath.Base(path), oldProjectExt)+projectExt))
	case projectExt:
	default:
		return problems
	}

	project, err := loadProject(path)
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

func (c *Config) applyEnv(env map[string]string) error {
	err := errors.Join(
		overlayWhole(env, envMaxConcurrent, "for example 2.", &c.Agents.MaxConcurrent),
		overlayWhole(env, envMaxReviewConcurrent, "for example 2.", &c.Agents.MaxReviewConcurrent),
		overlayWhole(env, envMaxTurns, "for example 40, or 0 for no cap.", &c.Agents.MaxTurns),
		overlayWhole(env, envReviewPoll, "for example 60, or 0 for no poll.", &c.Agents.ReviewPollMinutes),
		overlayWhole(env, envRetentionDays, "for example 30, or 0 to keep every finished run.", &c.Agents.RetentionDays),
		overlayDollars(env, envMaxBudget, "5", &c.Agents.MaxBudgetUSD),
		overlayDollars(env, envMaxRunBudget, "20", &c.Agents.MaxRunBudgetUSD),
	)
	if err != nil {
		return err
	}

	return errors.Join(
		overlayPercent(env, envFiveHourMax, &c.Pool.FiveHourMax),
		overlayPercent(env, envSevenDayMax, &c.Pool.SevenDayMax),
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

// limitProblems checks the two limits on agent processes.
func (c Config) limitProblems(runnersPath string) Problems {
	var problems Problems

	if c.Agents.MaxConcurrent < 1 {
		problems = append(problems, refuse("agents.max_concurrent must be 1 or more. Set it in %s or in %s.", runnersPath, envMaxConcurrent))
	}

	if c.Agents.MaxReviewConcurrent < 1 {
		problems = append(problems, refuse("agents.max_review_concurrent must be 1 or more. Set it in %s or in %s.", runnersPath, envMaxReviewConcurrent))
	}

	return problems
}

// Validate checks the values that no single file owns: the agent limit and the pool caps.
func (c Config) Validate() Problems {
	var problems Problems

	runnersPath := filepath.Join(c.Dir, runnersFileName)

	problems = append(problems, c.limitProblems(runnersPath)...)

	if c.Agents.MaxTurns < 0 {
		problems = append(problems, refuse("agents.max_turns must be 0 (no cap) or more. Set it in %s or in %s.", runnersPath, envMaxTurns))
	}

	problems = append(problems, c.budgetProblems(runnersPath)...)
	problems = append(problems, c.pollProblems(runnersPath)...)
	problems = append(problems, c.retentionProblems(runnersPath)...)

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

	for _, limit := range []struct {
		key, variable string
		percent       float64
	}{
		{"five_hour_max", envFiveHourMax, c.Pool.FiveHourMax},
		{"seven_day_max", envSevenDayMax, c.Pool.SevenDayMax},
	} {
		// The negated form also refuses NaN, which fails every comparison.
		if !(limit.percent > 0 && limit.percent <= maxPercent) {
			problems = append(problems, refuse("`%s` in the pool block must be a number above 0 and up to 100. Set it in %s or in %s.",
				limit.key, runnersPath, limit.variable))
		}
	}

	return problems
}
