package config

import (
	"time"

	"github.com/hashicorp/hcl/v2"

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

// rawStep is one steps "<name>" block as written.
// Each key has a range for the refusals that come after the decode. A key that is not in the block has an empty range.
type rawStep struct {
	At            hcl.Range `hcl:",def_range"`
	Name          string    `hcl:"name,label"`
	Runner        *string   `hcl:"runner,optional"`
	RunnerAt      hcl.Range `hcl:"runner,attr_name_range"`
	FreshRunner   *string   `hcl:"fresh_runner,optional"`
	FreshRunnerAt hcl.Range `hcl:"fresh_runner,attr_name_range"`
	Skills        *[]string `hcl:"skills,optional"`
	SkillsAt      hcl.Range `hcl:"skills,attr_name_range"`
	Timeout       *string   `hcl:"timeout,optional"`
	TimeoutAt     hcl.Range `hcl:"timeout,attr_name_range"`
	IdleTimeout   *string   `hcl:"idle_timeout,optional"`
	IdleTimeoutAt hcl.Range `hcl:"idle_timeout,attr_name_range"`
}

// override is a duration key as written, with the range of its name.
type override struct {
	key  string
	text *string
	at   hcl.Range
}

// applySteps merges the blocks of one file over the defaults.
func applySteps(base Steps, blocks []rawStep) (Steps, error) {
	merged := make(Steps, len(base))
	for step, settings := range base {
		merged[step] = settings
	}

	seen := make(map[string]bool, len(blocks))

	for _, block := range blocks {
		if seen[block.Name] {
			return nil, refuseAt(block.At, "steps %q appears twice. Keep one.", block.Name)
		}

		seen[block.Name] = true

		if err := applyStep(merged, block); err != nil {
			return nil, err
		}
	}

	return merged, nil
}

// applyStep merges one steps block into merged.
func applyStep(merged Steps, block rawStep) error {
	step, err := model.ParseStep(block.Name)
	if err != nil {
		return refuseAt(block.At, "steps %q: %w. Rename the block.", block.Name, err)
	}

	settings, err := mergeStep(step, merged[step], block)
	if err != nil {
		return err
	}

	merged[step] = settings

	return nil
}

// checkStepKeys refuses a table that holds a key its step does not take. The refusal points at the first such key.
func checkStepKeys(step model.Step, table rawStep) error {
	if !step.IsAgent() && (table.Runner != nil || table.Skills != nil || table.IdleTimeout != nil) {
		return refuseAt(earliest(table.RunnerAt, table.SkillsAt, table.IdleTimeoutAt),
			"steps %q: only plan, implement and review take runner, skills and idle_timeout. Remove them", step)
	}

	if table.FreshRunner != nil && step != model.StepImplement {
		return refuseAt(table.FreshRunnerAt,
			"steps %q: only implement takes fresh_runner, because fix round 4 runs as an implement step. Remove it", step)
	}

	return nil
}

// earliest returns the range that starts first. A key that is not in the file has an empty range and never wins.
func earliest(ranges ...hcl.Range) hcl.Range {
	var first hcl.Range

	for _, r := range ranges {
		if r.Filename != "" && (first.Filename == "" || r.Start.Byte < first.Start.Byte) {
			first = r
		}
	}

	return first
}

func mergeStep(step model.Step, settings StepSettings, table rawStep) (StepSettings, error) {
	if err := checkStepKeys(step, table); err != nil {
		return settings, err
	}

	settings = withNames(settings, table)

	var err error
	if settings.Timeout, err = duration(step, settings.Timeout, override{"timeout", table.Timeout, table.TimeoutAt}, !step.IsAgent()); err != nil {
		return settings, err
	}

	settings.IdleTimeout, err = duration(step, settings.IdleTimeout, override{"idle_timeout", table.IdleTimeout, table.IdleTimeoutAt}, step.IsAgent())

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
func duration(step model.Step, current time.Duration, set override, allowed bool) (time.Duration, error) {
	if set.text == nil {
		return current, nil
	}

	if !allowed || !step.HasDeadline() {
		return 0, refuseAt(set.at, "steps %q: %s does not apply to step %s. Remove it", step, set.key, step)
	}

	parsed, err := time.ParseDuration(*set.text)
	if err != nil || parsed <= 0 {
		return 0, refuseAt(set.at, "steps %q: %s = %q must be a positive duration such as \"30m\". Fix the value", step, set.key, *set.text)
	}

	return parsed, nil
}
