package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

// noSandbox is an adapter whose profile has no sandbox.
type noSandbox struct{ agent.Adapter }

func (a noSandbox) Profile() agent.Profile {
	profile := a.Adapter.Profile()
	profile.Sandbox = false

	return profile
}

// oddMinimum is an adapter whose profile names a minimum version that is no version number.
type oddMinimum struct{ agent.Adapter }

func (a oddMinimum) Profile() agent.Profile {
	profile := a.Adapter.Profile()
	profile.MinVersion = "soon"

	return profile
}

func seen(version string) func(context.Context, string, []string) (string, error) {
	return func(context.Context, string, []string) (string, error) { return version, nil }
}

func boxed(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, "claude", model.AgentClaude, "")
	f.spec.Sandbox = &agent.SandboxPlan{Work: f.work, Network: []string{"api.example.org"}}

	return f
}

func TestRunner_ASandboxedRunGetsTheSandboxArgumentsInOneSettingsObject(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := boxed(t)
	f.spec.Guard = "beehave check command"

	got := agent.Runner{Version: seen("2.1.316 (Claude Code)\n")}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)

	args := record(t, f.records, "args")
	assert.Contains(t, args, "--setting-sources  --strict-mcp-config --disallowedTools WebFetch WebSearch --settings {")
	assert.Contains(t, args, `"allowedDomains":["api.example.org"]`)
	assert.Contains(t, args, `"command":"beehave check command"`)
	assert.Equal(t, 1, countWord(args, "--settings"))
}

func TestRunner_ARunWithNoSandboxGetsNoSandboxArguments(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", model.AgentClaude, "")

	got := agent.Runner{}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.NotContains(t, record(t, f.records, "args"), "--setting-sources")
	assert.NotContains(t, record(t, f.records, "args"), "--strict-mcp-config")
}

func TestRunner_RefusesASandboxOnAnAdapterWithoutOne(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := boxed(t)
	f.spec.Adapter = noSandbox{f.spec.Adapter}

	got := agent.Runner{Version: seen("2.1.316")}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "claude has no sandbox, so a step with the sandbox on does not start")
	assert.Contains(t, got.Note, "sandbox = None")
}

func TestRunner_RefusesASandboxOnWindows(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := boxed(t)

	got := agent.Runner{OS: "windows", Version: seen("2.1.316")}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "Windows")
}

func TestRunner_RefusesASandboxWhenTheCLIIsOlderThanTheMinimum(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := boxed(t)
	got := agent.Runner{Version: seen("2.1.100 (Claude Code)")}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "claude is at version 2.1.100 and the sandbox needs")
	assert.NoFileExists(t, f.records+"/args", "the CLI never started")
}

func TestRunner_RefusesASandboxWhenTheMinimumVersionOfTheProfileIsNoVersion(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := boxed(t)
	f.spec.Adapter = oddMinimum{f.spec.Adapter}

	got := agent.Runner{Version: seen("claude 2.1.999")}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "not a version number")
}

func TestRunner_ReadsTheVersionFromTheCLIWhenNoReaderIsSet(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := boxed(t)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	// The fake CLI prints no version, so the run stops at the version check after it started the CLI with --version.
	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "is not readable")
}

func TestRunner_AVersionJustAtTheMinimumStarts(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := boxed(t)
	least := f.spec.Adapter.Profile().MinVersion

	got := agent.Runner{Version: seen("claude " + least)}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)
}

func TestRunner_TheMinimumVersionIsTheBoundaryOnEveryPart(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	least := boxed(t).spec.Adapter.Profile().MinVersion
	require.Equal(t, "2.1.285", least)

	for version, outcome := range map[string]agent.Outcome{
		"2.1.284":  agent.OutcomeNotStarted,
		"2.1.0":    agent.OutcomeNotStarted,
		"2.0.999":  agent.OutcomeNotStarted,
		"1.9.9999": agent.OutcomeNotStarted,
		"2.1.285":  agent.OutcomeExited,
		"2.1.286":  agent.OutcomeExited,
		"2.1.1000": agent.OutcomeExited,
		"2.2.0":    agent.OutcomeExited,
		"3.0.0":    agent.OutcomeExited,
		"2.1.0285": agent.OutcomeExited,
	} {
		f := boxed(t)

		got := agent.Runner{Version: seen(version + " (Claude Code)")}.Run(context.Background(), f.spec)

		assert.Equal(t, outcome, got.Outcome, version+": "+got.Note)
	}
}

func TestRunner_RefusesASandboxWhenTheVersionCannotBeRead(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := boxed(t)

	unreadable := agent.Runner{Version: seen("no number here")}.Run(context.Background(), f.spec)
	failing := agent.Runner{Version: func(context.Context, string, []string) (string, error) { return "", errors.New("boom") }}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, unreadable.Outcome)
	assert.Contains(t, unreadable.Note, "is not readable")
	assert.Equal(t, agent.OutcomeNotStarted, failing.Outcome)
	assert.Contains(t, failing.Note, "cannot read the version of claude")
}

func TestRunner_ASandboxedRunGetsNoProxyVariableNoAgentSocketAndNoGitConfig(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := boxed(t)
	f.spec.Env["HTTP_PROXY"], f.spec.Env["HTTPS_PROXY"], f.spec.Env["ALL_PROXY"] = "http://owner:3128", "http://owner:3128", "http://owner:3128"
	f.spec.Env["http_proxy"], f.spec.Env["https_proxy"], f.spec.Env["all_proxy"], f.spec.Env["NO_PROXY"], f.spec.Env["no_proxy"] = "p", "p", "p", "x", "x"
	f.spec.Env["SSH_AUTH_SOCK"] = "/tmp/agent.sock"

	got := agent.Runner{Version: seen("2.1.316")}.Run(context.Background(), f.spec)
	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)

	env := record(t, f.records, "env")
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "NO_PROXY", "no_proxy", "SSH_AUTH_SOCK"} {
		assert.NotContains(t, env, name+"=")
	}

	assert.Contains(t, env, "GIT_CONFIG_GLOBAL=/dev/null\n")
	assert.Contains(t, env, "GIT_CONFIG_NOSYSTEM=1\n")
	assert.Contains(t, env, "CLAUDE_CODE_OAUTH_TOKEN="+token+"\n")
}

func TestRunner_ARunWithNoSandboxKeepsTheProxyVariablesAndSetsNoGitVariable(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", model.AgentClaude, "")
	f.spec.Env["HTTPS_PROXY"] = "http://owner:3128"

	got := agent.Runner{}.Run(context.Background(), f.spec)
	require.Equal(t, agent.OutcomeExited, got.Outcome)

	env := record(t, f.records, "env")
	assert.Contains(t, env, "HTTPS_PROXY=http://owner:3128")
	assert.NotContains(t, env, "GIT_CONFIG_GLOBAL")
}

func countWord(text, word string) int {
	n := 0

	for _, field := range strings.Fields(text) {
		if field == word {
			n++
		}
	}

	return n
}
