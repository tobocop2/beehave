package agenttest_test

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/agent/agenttest"
	"github.com/tobocop2/beehest/internal/model"
)

// recorder is a Reporter that keeps the failures instead of failing the test.
type recorder struct {
	failures []string
}

func (*recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// fakeAdapter is a correct adapter. Each field of a flawed one switches on one wrong behavior.
type fakeAdapter struct {
	profile agent.Profile
	// invent makes Calls report a call and a limit on every line.
	invent bool
	// denies makes Calls report a denial, and nothing else, on every line.
	denies bool
	// refuses makes Calls report a refused call, and nothing else, on every line.
	refuses bool
	// resulting makes Calls report a result line, and nothing else, on every line.
	resulting bool
	// reads makes Calls read dollars from a line "spent <n>".
	reads bool
	// files is the Files of the launch of the guard and of the sandbox.
	files map[string]string
	// extra is the arguments that the launch of the guard and of the sandbox always adds.
	extra []string
	// closed makes the launch of the guard and of the sandbox leave each output file closed to the file tools.
	closed bool
	// transcript replaces the transcript path, <configDir>/<session>, when it is set.
	transcript func(configDir, session string) string
}

func (a fakeAdapter) Profile() agent.Profile { return a.profile }

func (a fakeAdapter) Guard(_, _ string, write, _ []string) agent.Launch { return a.launch(write) }

func (a fakeAdapter) Sandbox(_ agent.SandboxPlan, _, _ string, write, _ []string) agent.Launch {
	return a.launch(write)
}

// launch is the launch of the guard and of the sandbox: the files, and a working directory for each file of write unless the adapter leaves them closed.
func (a fakeAdapter) launch(write []string) agent.Launch {
	launch := agent.Launch{Files: a.files, Args: slices.Clone(a.extra)}

	if a.closed {
		return launch
	}

	for _, file := range write {
		launch.Args = append(launch.Args, "--add-dir", filepath.Dir(file))
	}

	return launch
}

func (fakeAdapter) Line(string) (string, string) { return "", "" }

func (fakeAdapter) ReadUsage(context.Context, model.Runner) model.Reading {
	return model.Unknown("the fake agent cannot report usage")
}

func (a fakeAdapter) TranscriptPath(configDir, _, session string) string {
	if a.transcript != nil {
		return a.transcript(configDir, session)
	}

	return filepath.Join(configDir, session)
}

func (a fakeAdapter) Calls(line string) agent.Seen {
	if a.reads && strings.HasPrefix(line, "spent ") {
		return agent.Seen{Spent: 1}
	}

	if a.invent {
		return agent.Seen{Shell: []agent.Call{{Text: "ls"}}, Skills: []agent.Call{{Text: "s"}}, Limit: &agent.Limit{Kind: agent.LimitRate}}
	}

	if a.denies {
		return agent.Seen{Denied: "paused"}
	}

	if a.refuses {
		return agent.Seen{Refused: []string{"rm -rf"}}
	}

	if a.resulting {
		return agent.Seen{Result: true}
	}

	return agent.Seen{}
}

func good() fakeAdapter {
	return fakeAdapter{
		profile: agent.Profile{Binary: "fake-cli", Args: []string{"-p", "--json"}, ConfigVars: []string{"FAKE_CONFIG_DIR"}, TokenVar: "FAKE_TOKEN"},
	}
}

func TestTestAdapter_PassesACorrectAdapter(t *testing.T) {
	t.Parallel()

	agenttest.TestAdapter(t, func() agent.Adapter { return good() })
}

func TestTestAdapter_PassesAnAdapterWithASupportedVersionRange(t *testing.T) {
	t.Parallel()

	// The fake CLI prints no version, so the suite must answer the version check with the first supported version, or no run would start.
	agenttest.TestAdapter(t, func() agent.Adapter {
		a := good()
		a.profile.SupportedFrom, a.profile.SupportedBefore = "3.0.0", "4.0.0"

		return a
	})
}

func TestCheckRun_AnAdapterWhoseRangeTheSuiteVersionMissesDoesNotRun(t *testing.T) {
	t.Parallel()

	a := good()
	a.profile.SupportedFrom, a.profile.SupportedBefore = "3.0.0", "3.0.0"

	var r recorder

	agenttest.CheckRun(&r, a, t.TempDir())

	require.NotEmpty(t, r.failures, "a range that holds no version stops every run")
}

const rangeFailure = "the profile sets one bound of the supported versions: set both SupportedFrom and SupportedBefore, or neither"

func TestCheckProfile(t *testing.T) {
	t.Parallel()

	flawed := func(change func(*agent.Profile)) fakeAdapter {
		a := good()
		change(&a.profile)

		return a
	}

	cases := []struct {
		name    string
		adapter fakeAdapter
		want    string
	}{
		{"a_correct_profile", good(), ""},
		{"no_binary", flawed(func(p *agent.Profile) { p.Binary = "" }), "the profile has no binary"},
		{"a_binary_that_is_a_path", flawed(func(p *agent.Profile) { p.Binary = "/usr/bin/fake" }), `the binary "/usr/bin/fake" is a path: the runner looks the name up on PATH`},
		{"both_bounds_of_a_range", flawed(func(p *agent.Profile) { p.SupportedFrom, p.SupportedBefore = "1.0.0", "2.0.0" }), ""},
		{"only_the_first_supported_version", flawed(func(p *agent.Profile) { p.SupportedFrom = "1.0.0" }), rangeFailure},
		{"only_the_first_unsupported_version", flawed(func(p *agent.Profile) { p.SupportedBefore = "2.0.0" }), rangeFailure},
		{"version_arguments_with_a_range", flawed(func(p *agent.Profile) {
			p.SupportedFrom, p.SupportedBefore = "1.0.0", "3.0.0"
			p.VersionArgs = []agent.VersionArgs{{From: "2.0.0", Args: []string{"--x"}}}
		}), ""},
		{
			"version_arguments_with_no_range", flawed(func(p *agent.Profile) { p.VersionArgs = []agent.VersionArgs{{From: "2.0.0", Args: []string{"--x"}}} }),
			"the profile sets VersionArgs or GuardBefore but no supported versions: the runner reads the version only for a profile with a range",
		},
		{
			"a_guard_limit_with_no_range", flawed(func(p *agent.Profile) { p.GuardBefore = "2.0.0" }),
			"the profile sets VersionArgs or GuardBefore but no supported versions: the runner reads the version only for a profile with a range",
		},
		{"no_login_variable", flawed(func(p *agent.Profile) { p.ConfigVars = nil }), "the profile has no login variable: the CLI would use the login of the caller"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			agenttest.CheckProfile(&r, tc.adapter)

			if tc.want == "" {
				assert.Empty(t, r.failures)

				return
			}

			assert.Contains(t, r.failures, tc.want)
		})
	}
}

func TestCheckRun(t *testing.T) {
	t.Parallel()

	t.Run("a_correct_adapter_passes", func(t *testing.T) {
		t.Parallel()

		var r recorder

		agenttest.CheckRun(&r, good(), t.TempDir())

		assert.Empty(t, r.failures)
	})

	t.Run("a_profile_without_a_token_variable_passes", func(t *testing.T) {
		t.Parallel()

		a := good()
		a.profile.TokenVar = ""

		var r recorder

		agenttest.CheckRun(&r, a, t.TempDir())

		assert.Empty(t, r.failures)
	})

	t.Run("a_login_variable_of_the_caller_is_a_failure", func(t *testing.T) {
		t.Parallel()

		a := good()
		a.profile.ConfigVars = []string{"BEEHEST_SUITE_LEAK"}

		var r recorder

		agenttest.CheckRun(&r, a, t.TempDir())

		assert.Contains(t, r.failures, "the CLI saw BEEHEST_SUITE_LEAK, a variable of the caller")
	})

	t.Run("a_stray_launch_argument_that_nobody_declared_is_a_failure", func(t *testing.T) {
		t.Parallel()

		a := good()
		a.extra = []string{"--stray"}

		var r recorder

		agenttest.CheckRun(&r, a, t.TempDir())

		require.Len(t, r.failures, 1)
		assert.Contains(t, r.failures[0], "the CLI did not get the arguments")
	})

	t.Run("a_declared_launch_argument_passes", func(t *testing.T) {
		t.Parallel()

		a := good()
		a.extra = []string{"--settings", "{}"}

		var r recorder

		agenttest.CheckRun(&r, a, t.TempDir(), "--settings", "{}")

		assert.Empty(t, r.failures)
	})

	t.Run("a_declared_launch_argument_that_the_adapter_never_adds_is_a_failure", func(t *testing.T) {
		t.Parallel()

		var r recorder

		agenttest.CheckRun(&r, good(), t.TempDir(), "--settings", "{}")

		require.Len(t, r.failures, 1)
		assert.Contains(t, r.failures[0], "the CLI did not get the arguments")
	})

	t.Run("a_folder_that_does_not_exist_is_a_failure", func(t *testing.T) {
		t.Parallel()

		var r recorder

		agenttest.CheckRun(&r, good(), filepath.Join(t.TempDir(), "none"))

		assert.Len(t, r.failures, 1)
		assert.Contains(t, r.failures[0], "cannot write the fake CLI")
	})
}

func TestCheckLaunch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"no_file", nil, ""},
		{"a_file_in_a_folder_of_the_run_folder", map[string]string{"conf/hooks.json": "x"}, ""},
		{"a_path_with_dot_dot", map[string]string{"../hooks.json": "x"}, `the launch file "../hooks.json" is outside the run folder`},
		{"a_path_that_climbs_inside", map[string]string{"a/../../b": "x"}, `the launch file "a/../../b" is outside the run folder`},
		{"an_absolute_path", map[string]string{"/etc/hooks.json": "x"}, `the launch file "/etc/hooks.json" is outside the run folder`},
		{"an_empty_path", map[string]string{"": "x"}, `the launch file "" is outside the run folder`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a := good()
			a.files = tc.files
			a.profile.Sandbox = true

			var r recorder

			agenttest.CheckLaunch(&r, a)

			if tc.want == "" {
				assert.Empty(t, r.failures)

				return
			}

			assert.Contains(t, r.failures, tc.want+" (guard)")
			assert.Contains(t, r.failures, tc.want+" (sandbox)")
		})
	}
}

func TestCheckLaunch_AnAdapterThatLeavesTheOutputFileClosedFailsInEveryLaunchMode(t *testing.T) {
	t.Parallel()

	a := good()
	a.closed = true
	a.profile.Sandbox = true

	var r recorder

	agenttest.CheckLaunch(&r, a)

	const text = "never names the folder of the output file /state/outputs/1.step/output.json, so the file tools cannot read it"

	assert.Equal(t, []string{"the launch (guard) " + text, "the launch (launch with no guard) " + text, "the launch (sandbox) " + text}, r.failures)
}

func TestCheckLaunch_DoesNotAskAProfileWithoutASandboxForOne(t *testing.T) {
	t.Parallel()

	a := good()
	a.files = map[string]string{"../x": "x"}

	var r recorder

	agenttest.CheckLaunch(&r, a)

	assert.Equal(t, []string{`the launch file "../x" is outside the run folder (guard)`}, r.failures)
}

func TestTestAdapter_PassesAnAdapterWhoseLaunchAddsTheArgumentsItDeclares(t *testing.T) {
	t.Parallel()

	a := good()
	a.extra = []string{"--settings", "{}"}

	agenttest.TestAdapter(t, func() agent.Adapter { return a }, agenttest.WithLaunchArgs("--settings", "{}"))
}

func TestTestAdapter_PassesAnAdapterThatStreamsItsCostWhenGivenACostLine(t *testing.T) {
	t.Parallel()

	a := good()
	a.profile.StreamsCost, a.reads = true, true

	agenttest.TestAdapter(t, func() agent.Adapter { return a }, agenttest.WithCostLine("spent 1"))
}

func TestCheckNoEdits(t *testing.T) {
	t.Parallel()

	t.Run("a_profile_with_no_edit_arguments_and_variables_passes", func(t *testing.T) {
		t.Parallel()

		a := good()
		a.profile.EditArgs = []string{"--edit"}
		a.profile.NoEditArgs = []string{"--read-only"}
		a.profile.NoEditEnv = map[string]string{"FAKE_PERMISSION": "deny"}

		var r recorder

		agenttest.CheckNoEdits(&r, a, t.TempDir())

		assert.Empty(t, r.failures)
	})

	t.Run("a_stray_launch_argument_of_a_step_with_no_edits_is_a_failure", func(t *testing.T) {
		t.Parallel()

		a := good()
		a.extra = []string{"--stray"}

		var r recorder

		agenttest.CheckNoEdits(&r, a, t.TempDir())

		require.Len(t, r.failures, 1)
		assert.Contains(t, r.failures[0], "the CLI of a step with no edits did not get the arguments")
	})

	t.Run("a_declared_launch_argument_of_a_step_with_no_edits_passes", func(t *testing.T) {
		t.Parallel()

		a := good()
		a.extra = []string{"--settings", "{}"}

		var r recorder

		agenttest.CheckNoEdits(&r, a, t.TempDir(), "--settings", "{}")

		assert.Empty(t, r.failures)
	})

	t.Run("a_no_edit_variable_that_is_a_login_variable_fails", func(t *testing.T) {
		t.Parallel()

		a := good()
		a.profile.NoEditEnv = map[string]string{"FAKE_CONFIG_DIR": "deny"}

		var r recorder

		agenttest.CheckNoEdits(&r, a, t.TempDir())

		assert.Contains(t, r.failures, "the no-edit variable FAKE_CONFIG_DIR is a login variable of the profile")
	})
}

func TestCheckBudget(t *testing.T) {
	t.Parallel()

	streams := func() fakeAdapter {
		a := good()
		a.profile.StreamsCost, a.reads = true, true

		return a
	}

	cases := []struct {
		name    string
		adapter fakeAdapter
		line    string
		want    string
	}{
		{"a_profile_with_no_cap_refuses_the_run", good(), "", ""},
		{"a_profile_that_streams_its_cost", streams(), "spent 1", ""},
		{"a_profile_that_streams_its_cost_with_no_cost_line", streams(), "", "the profile sets StreamsCost, but the suite has no line that reports dollars: pass WithCostLine"},
		{"a_cost_line_that_calls_does_not_read", func() fakeAdapter { a := streams(); a.reads = false; return a }(), "spent 1", `the line "spent 1" reports dollars, but Calls returns Spent 0`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			agenttest.CheckBudget(&r, tc.adapter, t.TempDir(), tc.line)

			if tc.want == "" {
				assert.Empty(t, r.failures)

				return
			}

			assert.Contains(t, r.failures, tc.want)
		})
	}

	t.Run("a_profile_with_a_cost_flag_gets_the_cap", func(t *testing.T) {
		t.Parallel()

		a := good()
		a.profile.BudgetFlag = "--max-budget"

		var r recorder

		agenttest.CheckBudget(&r, a, t.TempDir(), "")

		assert.Empty(t, r.failures)
	})
}

func TestCheckCalls(t *testing.T) {
	t.Parallel()

	var r recorder

	agenttest.CheckCalls(&r, good())

	assert.Empty(t, r.failures)

	a := good()
	a.invent = true

	agenttest.CheckCalls(&r, a)

	require.Len(t, r.failures, 3)
	assert.Contains(t, r.failures[0], "shell calls")
	assert.Contains(t, r.failures[1], "skill calls")
	assert.Contains(t, r.failures[2], "a limit")
}

func TestTestAdapter_PassesAnAdapterThatResumesASession(t *testing.T) {
	t.Parallel()

	a := good()
	a.profile.Resume = func(id string) []string { return []string{"--resume", id} }

	agenttest.TestAdapter(t, func() agent.Adapter { return a })
}

func TestCheckTranscript(t *testing.T) {
	t.Parallel()

	resumes := func(transcript func(configDir, session string) string) fakeAdapter {
		a := good()
		a.profile.Resume = func(id string) []string { return []string{"--resume", id} }
		a.transcript = transcript

		return a
	}
	option := func(resume func(id string) []string) fakeAdapter {
		a := resumes(nil)
		a.profile.Resume = resume

		return a
	}

	const unnamed = "does not name the session"

	cases := []struct {
		name    string
		adapter fakeAdapter
		want    string
	}{
		{"a_resumable_session_with_its_transcript_in_the_login_folder", resumes(nil), ""},
		{"a_resume_option_of_one_token_names_the_session", func() fakeAdapter {
			a := resumes(nil)
			a.profile.Resume = func(id string) []string { return []string{"--resume=" + id} }

			return a
		}(), ""},
		{"a_profile_with_no_resume_option_is_not_asked", func() fakeAdapter {
			a := good()
			a.transcript = func(string, string) string { return "/elsewhere" }

			return a
		}(), ""},
		{"a_transcript_outside_the_login_folder", resumes(func(_, session string) string { return filepath.Join("/elsewhere", session) }), "is outside the login folder"},
		{"one_transcript_for_every_session", resumes(func(configDir, _ string) string { return filepath.Join(configDir, "transcript") }), "names one file for the sessions"},
		{"a_resume_option_that_does_not_name_the_session", func() fakeAdapter {
			a := resumes(nil)
			a.profile.Resume = func(string) []string { return []string{"--continue"} }

			return a
		}(), unnamed},
		{"a_resume_option_with_the_file_name_of_the_session", func() fakeAdapter {
			a := resumes(nil)
			a.profile.Resume = func(id string) []string { return []string{"--resume", id + ".jsonl"} }

			return a
		}(), unnamed},
		{"a_resume_option_with_a_prefix_on_the_session", func() fakeAdapter {
			a := resumes(nil)
			a.profile.Resume = func(id string) []string { return []string{"--resume", "ses_" + id} }

			return a
		}(), unnamed},
		{"a_resume_option_of_one_token_with_the_file_name_of_the_session", func() fakeAdapter {
			a := resumes(nil)
			a.profile.Resume = func(id string) []string { return []string{"--resume=" + id + ".jsonl"} }

			return a
		}(), unnamed},
		{"an_option_whose_value_after_the_first_equals_sign_is_not_the_session", func() fakeAdapter {
			a := resumes(nil)
			a.profile.Resume = func(id string) []string { return []string{"--a=b=" + id} }

			return a
		}(), unnamed},
		{"a_resume_option_with_an_argument_after_the_session", option(func(id string) []string { return []string{"--resume", id, "--fork-session"} }), ""},
		{"a_resume_option_with_the_session_in_another_case", option(func(id string) []string { return []string{"--resume", strings.ToUpper(id)} }), unnamed},
		{"a_resume_option_of_one_token_with_the_session_in_another_case", option(func(id string) []string { return []string{"--resume=" + strings.ToUpper(id)} }), unnamed},
		{"a_resume_option_with_a_space_before_the_session", option(func(id string) []string { return []string{"--resume", " " + id} }), unnamed},
		{"a_resume_option_with_a_space_after_the_session", option(func(id string) []string { return []string{"--resume", id + " "} }), unnamed},
		{"a_resume_option_of_one_token_with_a_space_before_the_session", option(func(id string) []string { return []string{"--resume= " + id} }), unnamed},
		{"a_resume_option_of_one_token_with_a_space_after_the_session", option(func(id string) []string { return []string{"--resume=" + id + " "} }), unnamed},
		{"a_resume_option_and_the_session_in_one_token_with_a_space", option(func(id string) []string { return []string{"--resume " + id} }), unnamed},
		{"a_resume_option_of_one_token_with_quotation_marks_on_the_session", option(func(id string) []string { return []string{`--resume="` + id + `"`} }), unnamed},
		{"a_resume_option_with_a_part_of_the_session", option(func(id string) []string { return []string{"--resume", id[:len(id)-1]} }), unnamed},
		{"a_resume_option_of_one_token_with_a_part_of_the_session", option(func(id string) []string { return []string{"--resume=" + id[:len(id)-1]} }), unnamed},
		{"the_session_with_the_dashes_of_an_option", option(func(id string) []string { return []string{"--" + id} }), unnamed},
		{"an_option_with_no_name_before_the_equals_sign", option(func(id string) []string { return []string{"=" + id} }), unnamed},
		{"an_option_with_dashes_and_no_name_before_the_equals_sign", option(func(id string) []string { return []string{"--=" + id} }), unnamed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var r recorder

			agenttest.CheckTranscript(&r, tc.adapter, t.TempDir())

			if tc.want == "" {
				assert.Empty(t, r.failures)

				return
			}

			require.Len(t, r.failures, 1)
			assert.Contains(t, r.failures[0], tc.want)
		})
	}
}

func TestCheckCalls_ADenialAloneIsAFailure(t *testing.T) {
	t.Parallel()

	a := good()
	a.denies = true

	var r recorder

	agenttest.CheckCalls(&r, a)

	require.Len(t, r.failures, 1)
	assert.Contains(t, r.failures[0], "Denied:paused")
}

func TestCheckCalls_ARefusedCallAloneIsAFailure(t *testing.T) {
	t.Parallel()

	a := good()
	a.refuses = true

	var r recorder

	agenttest.CheckCalls(&r, a)

	require.Len(t, r.failures, 1)
	assert.Contains(t, r.failures[0], "Refused:[rm -rf]")
}

func TestCheckCalls_AResultLineAloneIsAFailure(t *testing.T) {
	t.Parallel()

	a := good()
	a.resulting = true

	var r recorder

	agenttest.CheckCalls(&r, a)

	require.Len(t, r.failures, 1)
	assert.Contains(t, r.failures[0], "Result:true")
}

func TestTheSuiteReportsAFakeCLIThatCannotBeWritten(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing")

	for name, check := range map[string]func(*recorder){
		"CheckNoEdits": func(r *recorder) { agenttest.CheckNoEdits(r, good(), missing) },
		"CheckBudget":  func(r *recorder) { agenttest.CheckBudget(r, good(), missing, "") },
	} {
		var r recorder

		check(&r)

		require.Len(t, r.failures, 1, name)
		assert.Contains(t, r.failures[0], "cannot write the fake CLI", name)
	}
}
