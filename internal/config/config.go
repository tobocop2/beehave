// Package config loads the config folder and the state folder.
// It is the only package that reads the process environment.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// Names of the OBEE_* variables that override the config files.
const (
	envMaxConcurrent = "OBEE_AGENTS_MAX_CONCURRENT"
	envFiveHourMax   = "OBEE_POOL_FIVE_HOUR_MAX"
	envSevenDayMax   = "OBEE_POOL_SEVEN_DAY_MAX"
)

// floatBits is the bit size of a percent that the OBEE_* variables give.
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

// Error joins the problems, one on each line.
func (p Problems) Error() string {
	lines := make([]string, len(p))
	for i, problem := range p {
		lines[i] = problem.Error()
	}

	return strings.Join(lines, "\n")
}

// DefaultConfig returns the config of an empty folder.
func DefaultConfig() Config {
	return Config{
		Agents:   agents{MaxConcurrent: defaultConcurrent},
		Pool:     Pool{FiveHourMax: defaultFiveHourMax, SevenDayMax: defaultSevenDayMax},
		Projects: map[string]Project{},
	}
}

// LoadFolder loads runners.hcl and every projects/*.hcl of a config folder, applies the OBEE_*
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
	path := filepath.Join(c.Dir, runnersFileName)
	if !isFile(path) {
		return nil
	}

	var err error
	c.Agents, c.Pool, c.Runners, err = loadRunnersFile(path)

	return err
}

// loadProjects reads projects/*.hcl in name order. A missing projects folder holds no project.
func (c *Config) loadProjects() Problems {
	dir := filepath.Join(c.Dir, projectsDirName)

	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Problems{refuse("cannot read %s: %w. Check that you can read the folder.", dir, err)}
	}

	var problems Problems

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == projectExt {
			problems = c.addProject(problems, filepath.Join(dir, entry.Name()))
		}
	}

	return problems
}

func (c *Config) addProject(problems Problems, path string) Problems {
	project, err := loadProject(path)
	if err != nil {
		return append(problems, err)
	}

	c.Projects[project.Name] = project

	return problems
}

func (c *Config) applyEnv(env map[string]string) error {
	if value := env[envMaxConcurrent]; value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil {
			return refuse("%s=%s is not a whole number. Write digits only, for example 2.", envMaxConcurrent, value)
		}

		c.Agents.MaxConcurrent = limit
	}

	return errors.Join(
		overlayPercent(env, envFiveHourMax, &c.Pool.FiveHourMax),
		overlayPercent(env, envSevenDayMax, &c.Pool.SevenDayMax),
	)
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

// Validate checks the values that no single file owns: the agent limit and the pool caps.
func (c Config) Validate() Problems {
	var problems Problems

	runnersPath := filepath.Join(c.Dir, runnersFileName)

	if c.Agents.MaxConcurrent < 1 {
		problems = append(problems, refuse("agents.max_concurrent must be 1 or more. Set it in %s or in %s.", runnersPath, envMaxConcurrent))
	}

	return append(problems, c.poolProblems(runnersPath)...)
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
