package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
)

func TestDefaultSteps_AreTheLimitsOfTheDefaultWorkflow(t *testing.T) {
	t.Parallel()

	steps := config.DefaultSteps()

	require.Len(t, steps, len(model.Steps()))

	want := map[model.Step][2]time.Duration{
		model.StepClaim:     {2 * time.Minute, 0},
		model.StepClaimGate: {time.Minute, 0},
		model.StepPlan:      {0, 10 * time.Minute},
		model.StepImplement: {0, 30 * time.Minute},
		model.StepGate:      {time.Hour, 0},
		model.StepRound:     {time.Minute, 0},
		model.StepReview:    {0, 30 * time.Minute},
		model.StepRecord:    {2 * time.Minute, 0},
		model.StepOwnerWait: {0, 0},
		model.StepReady:     {5 * time.Minute, 0},
		model.StepIntegrate: {5 * time.Minute, 0},
		model.StepRelease:   {2 * time.Minute, 0},
	}

	for _, step := range model.Steps() {
		got := [2]time.Duration{steps[step].Timeout, steps[step].IdleTimeout}
		require.Equal(t, want[step], got, step)
		require.Empty(t, steps[step].Runner, step)
	}

	require.Equal(t, []string{}, steps[model.StepImplement].Skills)
}

func TestDefaultSteps_ReturnsAFreshMapEachTime(t *testing.T) {
	t.Parallel()

	first := config.DefaultSteps()
	first[model.StepGate] = config.StepSettings{Timeout: time.Second}

	require.Equal(t, time.Hour, config.DefaultSteps()[model.StepGate].Timeout)
}

func TestLoadFolder_StepOverrides(t *testing.T) {
	t.Parallel()

	runners := goodRunner
	project := goodProject +
		"[steps.gate]\ntimeout = \"2h\"\n" +
		"[steps.implement]\nrunner = \"a\"\nskills = [\"tdd\", \"review\"]\nidle_timeout = \"45m\"\n" +
		"[steps.plan]\nrunner = \"a\"\n"

	dir := t.TempDir()
	writeFile(t, dir+"/runners.toml", runners)
	writeFile(t, dir+"/projects/p.toml", project)

	cfg, problems := config.LoadFolder(dir, nil)

	require.Empty(t, problems)

	steps := cfg.Projects["p"].Steps
	require.Equal(t, 2*time.Hour, steps[model.StepGate].Timeout)
	require.Equal(t, config.StepSettings{Runner: "a", Skills: []string{"tdd", "review"}, IdleTimeout: 45 * time.Minute}, steps[model.StepImplement])
	require.Equal(t, config.StepSettings{Runner: "a", IdleTimeout: 10 * time.Minute}, steps[model.StepPlan], "an unset key keeps the default")
	require.Equal(t, 5*time.Minute, steps[model.StepReady].Timeout)
}

func TestLoadFolder_ASkillsListMayBeEmptyAndOverrideTheDefault(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, goodProject+"[steps.review]\nskills = []\n")

	require.Empty(t, problems)
	require.Equal(t, []string{}, project.Steps[model.StepReview].Skills)
}

func TestLoadFolder_ABadStepTableIsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		table string
		words string
	}{
		{"unknown_step", "[steps.deploy]\ntimeout = \"1m\"\n", "[steps.deploy]: unknown step \"deploy\": use one of claim, claim-gate"},
		{"unknown_key", "[steps.gate]\nretries = 3\n", "unknown key `steps.gate.retries`"},
		{"unknown_key_lists_the_allowed_ones", "[steps.gate]\nretries = 3\n", "runner, skills, timeout, idle_timeout"},
		{"timeout_of_the_wrong_type", "[steps.gate]\ntimeout = 60\n", "`steps.gate.timeout` has the wrong type"},
		{"zero_timeout", "[steps.gate]\ntimeout = \"0s\"\n", `timeout = "0s" must be a positive duration`},
		{"negative_timeout", "[steps.gate]\ntimeout = \"-5m\"\n", `timeout = "-5m" must be a positive duration`},
		{"text_that_is_not_a_duration", "[steps.gate]\ntimeout = \"soon\"\n", `timeout = "soon" must be a positive duration`},
		{"idle_timeout_on_a_command_step", "[steps.gate]\nidle_timeout = \"5m\"\n", "only plan, implement and review take runner, skills and idle_timeout"},
		{"runner_on_a_command_step", "[steps.ready]\nrunner = \"a\"\n", "only plan, implement and review take runner"},
		{"skills_on_a_command_step", "[steps.ready]\nskills = []\n", "only plan, implement and review take runner"},
		{"timeout_on_an_agent_step", "[steps.plan]\ntimeout = \"5m\"\n", "timeout does not apply to step plan"},
		{"idle_timeout_on_an_agent_step_must_parse", "[steps.plan]\nidle_timeout = \"x\"\n", `idle_timeout = "x" must be a positive duration`},
		{"timeout_on_the_owner_wait", "[steps.owner-wait]\ntimeout = \"5m\"\n", "timeout does not apply to step owner-wait"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, goodProject+tc.table)

			require.Len(t, problems, 1)
			require.ErrorIs(t, problems[0], config.ErrRefused)
			require.ErrorContains(t, problems[0], tc.words)
			require.ErrorContains(t, problems[0], "p.toml")
		})
	}
}

func TestLoadFolder_FreshRunnerBelongsToImplementAndMustBeDefined(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		table string
		want  string
	}{
		{"accepts_a_defined_runner", "[steps.implement]\nfresh_runner = \"a\"\n", ""},
		{"refuses_an_unknown_runner", "[steps.implement]\nfresh_runner = \"ghost\"\n", "names fresh_runner `ghost`"},
		{"refuses_a_step_that_never_has_a_fix_round", "[steps.review]\nfresh_runner = \"a\"\n", "only implement takes fresh_runner"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			writeFile(t, dir+"/runners.toml", goodRunner)
			writeFile(t, dir+"/projects/p.toml", goodProject+test.table)

			cfg, problems := config.LoadFolder(dir, nil)
			if test.want == "" {
				require.Empty(t, problems)
				require.Equal(t, "a", cfg.Projects["p"].Steps[model.StepImplement].FreshRunner)

				return
			}

			require.Len(t, problems, 1)
			require.ErrorContains(t, problems[0], test.want)
		})
	}
}

func TestLoadFolder_AStepRunnerMustBeDefined(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir+"/runners.toml", goodRunner)
	writeFile(t, dir+"/projects/p.toml", goodProject+"[steps.review]\nrunner = \"ghost\"\n")

	_, problems := config.LoadFolder(dir, nil)

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "p.toml")
	require.ErrorContains(t, problems[0], "step `review` names runner `ghost`, which")
	require.ErrorContains(t, problems[0], "runners.toml does not define")

	writeFile(t, dir+"/projects/p.toml", goodProject+"[steps.review]\nrunner = \"a\"\n")
	_, problems = config.LoadFolder(dir, nil)
	require.Empty(t, problems)
}
