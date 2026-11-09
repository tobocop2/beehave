package opencode_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	costLine := fixtureLines(t, "ok.jsonl")[2]

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
	fixture, err := filepath.Abs("testdata/ok.jsonl")
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
	assert.InDelta(t, 0.000046, result.Streamed, 1e-12)
	assert.Len(t, strings.Split(strings.TrimSpace(out), "\n"), 6)
}

func TestRun_AStreamedCostAboveTheCapEndsTheRunAsABudgetCap(t *testing.T) {
	t.Parallel()

	result, _ := run(t, func(s *agent.Spec) { s.MaxBudget = 0.00003 })

	assert.Equal(t, agent.OutcomeBudgetCap, result.Outcome, result.Note)
}

// guarded writes a fake opencode that answers --version, replays a fixture, and writes the marker of the guard plugin when load is true.
func guarded(fixture, version string, load bool) string {
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo " + version + "; exit 0; fi\ncat > /dev/null\n"
	if load {
		script += "echo loaded > \"$OPENCODE_CONFIG_DIR/beehave-guard-loaded\"\n"
	}

	return script + "cat '" + fixture + "'\n"
}

func guardedRun(t *testing.T, script string, change func(*agent.Spec)) agent.Result {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(dir, "opencode"), script))

	spec := agent.Spec{
		Adapter: opencode.New(), Runner: model.Runner{Name: "r", Agent: model.AgentOpenCode, ConfigDir: dir},
		Dir: dir, Prompt: "go", Env: map[string]string{"PATH": dir + ":/bin:/usr/bin"}, Guard: "beehave check command",
	}
	change(&spec)

	return agent.Runner{}.Run(context.Background(), spec)
}

func absolute(t *testing.T, name string) string {
	t.Helper()

	path, err := filepath.Abs(name)
	require.NoError(t, err)

	return path
}

func TestRun_AGuardedRunWhoseCommandRanWithNoPluginIsFlagged(t *testing.T) {
	t.Parallel()

	// The recording of a run with the guard in force but no plugin loaded: the forbidden command ran, and the run exited with code 0.
	result := guardedRun(t, guarded(absolute(t, "testdata/noguard.jsonl"), "1.19.5", false), func(*agent.Spec) {})

	assert.Equal(t, agent.OutcomeExited, result.Outcome, result.Note)
	assert.Equal(t, 0, result.ExitCode)
	assert.True(t, result.GuardMissing, "a guard that did not load is never a silent pass")
}

func TestRun_AGuardedRunWhosePluginLoadedIsNotFlagged(t *testing.T) {
	t.Parallel()

	result := guardedRun(t, guarded(absolute(t, "testdata/refused.jsonl"), "1.19.5", true), func(*agent.Spec) {})

	assert.Equal(t, agent.OutcomeExited, result.Outcome, result.Note)
	assert.False(t, result.GuardMissing)
}

func TestRun_ARunWithNoGuardIsNeverFlagged(t *testing.T) {
	t.Parallel()

	result := guardedRun(t, guarded(absolute(t, "testdata/ok.jsonl"), "1.19.5", false), func(s *agent.Spec) { s.Guard = "" })

	assert.Equal(t, agent.OutcomeExited, result.Outcome, result.Note)
	assert.False(t, result.GuardMissing)
}

func TestRun_AGuardedRunOnAnOlderVersionDoesNotStart(t *testing.T) {
	t.Parallel()

	result := guardedRun(t, guarded(absolute(t, "testdata/ok.jsonl"), "1.19.4", true), func(*agent.Spec) {})

	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Contains(t, result.Note, "opencode is at version 1.19.4 and the command guard needs 1.19.5 or later")
	assert.Contains(t, result.Note, "forbid_commands")
}

func TestRun_AGuardedRunOnWindowsDoesNotStart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	result := agent.Runner{OS: "windows"}.Run(context.Background(), agent.Spec{
		Adapter: opencode.New(), Runner: model.Runner{Name: "r", Agent: model.AgentOpenCode, ConfigDir: dir},
		Dir: dir, Prompt: "p", Env: map[string]string{"PATH": dir}, Guard: "g",
	})

	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Contains(t, result.Note, "does not start on Windows")
}

func TestRun_ARunThatSetsNoTimeoutEndsAtTheProfileLimitAndNamesTheLikelyCause(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(dir, "opencode"), "#!/bin/sh\ncat > /dev/null\nsleep 30\n"))

	var limit time.Duration

	result := agent.Runner{AfterFunc: func(d time.Duration, fire func()) agent.Timer {
		limit = d

		return time.AfterFunc(20*time.Millisecond, fire)
	}}.Run(context.Background(), agent.Spec{
		Adapter: opencode.New(), Runner: model.Runner{Name: "r", Agent: model.AgentOpenCode, ConfigDir: dir},
		Dir: dir, Prompt: "p", Env: map[string]string{"PATH": dir + ":/bin:/usr/bin"},
	})

	assert.Equal(t, time.Hour, limit)
	assert.Equal(t, agent.OutcomeTimedOut, result.Outcome)
	assert.Contains(t, result.Note, "usage quota")
}

func TestProfile_PinsTheRecordedVersionAndRefusesWhatItCannotProve(t *testing.T) {
	t.Parallel()

	p := opencode.New().Profile()

	assert.Equal(t, "1.19.5", p.MinVersion)
	assert.Equal(t, "1.19.5", p.GuardMinVersion)
	assert.True(t, p.GuardPosixOnly)
	assert.True(t, p.NoSkillCalls)
	assert.Equal(t, time.Hour, p.DefaultTimeout)
}
