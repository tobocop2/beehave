package opencode_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/agenttest"
	"github.com/tobocop2/beehave/internal/agent/opencode"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
)

func TestAdapter_PassesTheConformanceSuite(t *testing.T) {
	t.Parallel()

	costLine := fixtureLines(t, "stream.jsonl")[3]

	agenttest.TestAdapter(t, func() agent.Adapter { return opencode.New() }, agenttest.WithCostLine(costLine))
}

func TestProfile_RunsJSONOutputWithNoPermissionAutoApprove(t *testing.T) {
	t.Parallel()

	p := opencode.New().Profile()

	assert.Equal(t, "opencode", p.Binary)
	assert.Equal(t, []string{"run", "--format", "json"}, p.Args)
	assert.NotContains(t, p.Args, "--auto", "an auto run would approve paths outside the work folder")
	assert.Equal(t, "--model", p.ModelFlag)
	assert.Equal(t, []string{"XDG_DATA_HOME"}, p.ConfigVars)
	assert.Empty(t, p.TokenVar)
}

func TestProfile_HasNoNativeCapsAndStreamsItsCost(t *testing.T) {
	t.Parallel()

	p := opencode.New().Profile()

	assert.Empty(t, p.TurnsFlag)
	assert.Empty(t, p.BudgetFlag)
	assert.True(t, p.StreamsCost)
}

func TestProfile_HasNoSandboxSoASandboxedStepIsRefused(t *testing.T) {
	t.Parallel()

	assert.False(t, opencode.New().Profile().Sandbox)

	dir := t.TempDir()

	result := agent.Runner{}.Run(context.Background(), agent.Spec{
		Adapter: opencode.New(), Runner: model.Runner{Name: "r", Agent: model.AgentOpenCode, ConfigDir: dir},
		Dir: dir, Prompt: "p", Env: map[string]string{"PATH": dir}, Sandbox: &agent.SandboxPlan{Work: dir},
	})

	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Contains(t, result.Note, "sandbox = None")
}

func TestProfile_NoEditEnvDeniesTheEditPermission(t *testing.T) {
	t.Parallel()

	p := opencode.New().Profile()

	assert.JSONEq(t, `{"edit":"deny"}`, p.NoEditEnv["OPENCODE_PERMISSION"])
	assert.Empty(t, p.NoEditArgs)
	assert.Empty(t, p.EditArgs)
}

func TestProfile_ResumeNamesTheSessionAndNeverContinuesTheLastOne(t *testing.T) {
	t.Parallel()

	p := opencode.New().Profile()

	assert.Equal(t, []string{"--session", "ses_1"}, p.Resume("ses_1"))
	assert.False(t, p.ResumeLast)
}

func TestTranscriptPath_IsTheDatabaseInTheLoginFolder(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/login/opencode/opencode.db", opencode.New().TranscriptPath("/login", "/work", "ses_1"))
}

// fakeEcho prints the arguments and then replays a fixture.
func fakeEcho(fixture string) string {
	return "#!/bin/sh\necho \"args=$*\" >&2\ncat > /dev/null\ncat '" + fixture + "'\n"
}

func run(t *testing.T, change func(*agent.Spec)) (agent.Result, string) {
	t.Helper()

	dir := t.TempDir()
	fixture, err := filepath.Abs("testdata/stream.jsonl")
	require.NoError(t, err)
	require.NoError(t, exectest.Write(filepath.Join(dir, "opencode"), fakeEcho(fixture)))

	var out bytes.Buffer

	spec := agent.Spec{
		Adapter: opencode.New(), Runner: model.Runner{Name: "r", Agent: model.AgentOpenCode, ConfigDir: dir},
		Dir: dir, Prompt: "go", Env: map[string]string{"PATH": dir + ":/bin:/usr/bin"}, Log: &out,
	}
	change(&spec)

	return agent.Runner{}.Run(context.Background(), spec), out.String()
}

func TestRun_AResumeNamesTheSessionInTheArguments(t *testing.T) {
	t.Parallel()

	result, _ := run(t, func(s *agent.Spec) { s.Resume = "ses_1"; s.Model = "p/m" })

	assert.Equal(t, agent.OutcomeExited, result.Outcome, result.Note)
	assert.Contains(t, result.Stderr, "args=run --format json --model p/m --session ses_1")
}

func TestRun_TheRunnerAddsTheStreamedDollarsOfTheReplay(t *testing.T) {
	t.Parallel()

	result, out := run(t, func(s *agent.Spec) { s.MaxBudget = 1 })

	assert.Equal(t, agent.OutcomeExited, result.Outcome, result.Note)
	assert.InDelta(t, 0.03, result.Streamed, 1e-9)
	assert.Len(t, strings.Split(strings.TrimSpace(out), "\n"), 7)
}

func TestRun_AStreamedCostAboveTheCapEndsTheRunAsABudgetCap(t *testing.T) {
	t.Parallel()

	result, _ := run(t, func(s *agent.Spec) { s.MaxBudget = 0.015 })

	assert.Equal(t, agent.OutcomeBudgetCap, result.Outcome, result.Note)
}
