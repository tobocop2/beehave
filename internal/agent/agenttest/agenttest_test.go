package agenttest_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/agent/agenttest"
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
}

func (a fakeAdapter) Profile() agent.Profile { return a.profile }

func (fakeAdapter) Guard(string) []string { return nil }

func (fakeAdapter) Sandbox(agent.SandboxPlan, string) []string { return nil }

func (fakeAdapter) Line(string) (string, string) { return "", "" }

func (fakeAdapter) TranscriptPath(configDir, _, session string) string {
	return filepath.Join(configDir, session)
}

func (a fakeAdapter) Calls(string) agent.Seen {
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
		a.profile.ConfigVars = []string{"OBEE_SUITE_LEAK"}

		var r recorder

		agenttest.CheckRun(&r, a, t.TempDir())

		assert.Contains(t, r.failures, "the CLI saw OBEE_SUITE_LEAK, a variable of the caller")
	})

	t.Run("a_folder_that_does_not_exist_is_a_failure", func(t *testing.T) {
		t.Parallel()

		var r recorder

		agenttest.CheckRun(&r, good(), filepath.Join(t.TempDir(), "none"))

		assert.Len(t, r.failures, 1)
		assert.Contains(t, r.failures[0], "cannot write the fake CLI")
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
