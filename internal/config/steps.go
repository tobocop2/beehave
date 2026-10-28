package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/tobocop2/obee/internal/model"
)

// StepSettings holds the settings of one workflow step. A zero duration means the step has no
// limit of that kind.
type StepSettings struct {
	Runner string
	// FreshRunner names the runner of the fresh agent that fix round 4 starts. Empty means the same runner.
	FreshRunner string
	Skills      []string
	Timeout     time.Duration
	IdleTimeout time.Duration
}

// Steps maps every workflow step to its settings.
type Steps map[model.Step]StepSettings

const (
	claimTimeout     = 2 * time.Minute
	claimGateTimeout = time.Minute
	planIdle         = 10 * time.Minute
	implementIdle    = 30 * time.Minute
	gateTimeout      = time.Hour
	roundTimeout     = time.Minute
	reviewIdle       = 30 * time.Minute
	recordTimeout    = 2 * time.Minute
	readyTimeout     = 5 * time.Minute
	integrateTimeout = 5 * time.Minute
	releaseTimeout   = 2 * time.Minute
)

// DefaultSteps returns the settings every project starts from. The values are the limits of the
// default workflow.
func DefaultSteps() Steps {
	return Steps{
		model.StepClaim:     {Timeout: claimTimeout},
		model.StepClaimGate: {Timeout: claimGateTimeout},
		model.StepPlan:      {IdleTimeout: planIdle},
		model.StepImplement: {IdleTimeout: implementIdle, Skills: []string{}},
		model.StepGate:      {Timeout: gateTimeout},
		model.StepRound:     {Timeout: roundTimeout},
		model.StepReview:    {IdleTimeout: reviewIdle},
		model.StepRecord:    {Timeout: recordTimeout},
		model.StepOwnerWait: {},
		model.StepReady:     {Timeout: readyTimeout},
		model.StepIntegrate: {Timeout: integrateTimeout},
		model.StepRelease:   {Timeout: releaseTimeout},
	}
}

// rawStep is one [steps.<name>] table as written.
type rawStep struct {
	Runner      *string   `toml:"runner"`
	FreshRunner *string   `toml:"fresh_runner"`
	Skills      *[]string `toml:"skills"`
	Timeout     *string   `toml:"timeout"`
	IdleTimeout *string   `toml:"idle_timeout"`
}

const stepKeys = "runner, fresh_runner, skills, timeout, idle_timeout"

// applySteps merges the tables of one file over the defaults. path names the file in messages.
func applySteps(path string, base Steps, tables map[string]rawStep) (Steps, error) {
	merged := make(Steps, len(base))
	for step, settings := range base {
		merged[step] = settings
	}

	for name, table := range tables {
		step, err := model.ParseStep(name)
		if err != nil {
			return nil, refuse("%s: [steps.%s]: %w. Rename the table.", path, name, err)
		}

		settings, err := mergeStep(step, merged[step], table)
		if err != nil {
			return nil, refuse("%s: [steps.%s]: %w", path, step, err)
		}

		merged[step] = settings
	}

	return merged, nil
}

// checkStepKeys refuses a table that holds a key its step does not take.
func checkStepKeys(step model.Step, table rawStep) error {
	if !step.IsAgent() && (table.Runner != nil || table.Skills != nil || table.IdleTimeout != nil) {
		return errors.New("only plan, implement and review take runner, skills and idle_timeout. Remove them")
	}

	if table.FreshRunner != nil && step != model.StepImplement {
		return errors.New("only implement takes fresh_runner, because fix round 4 runs as an implement step. Remove it")
	}

	return nil
}

func mergeStep(step model.Step, settings StepSettings, table rawStep) (StepSettings, error) {
	if err := checkStepKeys(step, table); err != nil {
		return settings, err
	}

	settings = withNames(settings, table)

	var err error
	if settings.Timeout, err = duration(table.Timeout, settings.Timeout, step, "timeout", !step.IsAgent()); err != nil {
		return settings, err
	}

	settings.IdleTimeout, err = duration(table.IdleTimeout, settings.IdleTimeout, step, "idle_timeout", step.IsAgent())

	return settings, err
}

// withNames applies the runner, fresh runner and skills a table sets.
func withNames(settings StepSettings, table rawStep) StepSettings {
	if table.Runner != nil {
		settings.Runner = *table.Runner
	}

	if table.FreshRunner != nil {
		settings.FreshRunner = *table.FreshRunner
	}

	if table.Skills != nil {
		settings.Skills = *table.Skills
	}

	return settings
}

// duration parses an override. A key the step does not take is refused with the fix.
func duration(text *string, current time.Duration, step model.Step, key string, allowed bool) (time.Duration, error) {
	if text == nil {
		return current, nil
	}

	if !allowed || !step.HasDeadline() {
		return 0, fmt.Errorf("%s does not apply to step %s. Remove it", key, step)
	}

	parsed, err := time.ParseDuration(*text)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s = %q must be a positive duration such as \"30m\". Fix the value", key, *text)
	}

	return parsed, nil
}
