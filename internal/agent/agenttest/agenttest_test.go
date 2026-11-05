package agenttest_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/agenttest"
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
	// invent makes Calls report a call on every line.
	invent bool
	// reads makes Calls read dollars from a line "spent <n>".
	reads bool
	// files is the Files of the launch of the guard and of the sandbox.
	files map[string]string
}

func (a fakeAdapter) Profile() agent.Profile { return a.profile }

func (a fakeAdapter) Guard(string, string) agent.Launch { return agent.Launch{Files: a.files} }

func (a fakeAdapter) Sandbox(agent.SandboxPlan, string, string) agent.Launch {
	return agent.Launch{Files: a.files}
}

func (fakeAdapter) Line(string) (string, string) { return "", "" }

func (fakeAdapter) TranscriptPath(configDir, _, session string) string {
	return filepath.Join(configDir, session)
}

func (a fakeAdapter) Calls(line string) agent.Seen {
	if a.reads && strings.HasPrefix(line, "spent ") {
		return agent.Seen{Spent: 1}
	}

	if a.invent {
		return agent.Seen{Shell: []agent.Call{{Text: "ls"}}, Skills: []string{"s"}}
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
		a.profile.ConfigVars = []string{"BEEHAVE_SUITE_LEAK"}

		var r recorder

		agenttest.CheckRun(&r, a, t.TempDir())

		assert.Contains(t, r.failures, "the CLI saw BEEHAVE_SUITE_LEAK, a variable of the caller")
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

func TestCheckLaunch_DoesNotAskAProfileWithoutASandboxForOne(t *testing.T) {
	t.Parallel()

	a := good()
	a.files = map[string]string{"../x": "x"}

	var r recorder

	agenttest.CheckLaunch(&r, a)

	assert.Equal(t, []string{`the launch file "../x" is outside the run folder (guard)`}, r.failures)
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

	require.Len(t, r.failures, 2)
	assert.Contains(t, r.failures[0], "shell calls")
	assert.Contains(t, r.failures[1], "skill calls")
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
