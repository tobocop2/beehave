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

func TestAgent_AnAdapterWithNoSandboxEndsItsStepNotStartedInASandboxedRun(t *testing.T) {
	t.Parallel()

	assert.False(t, opencode.New().Profile().Sandbox)

	dir := t.TempDir()

	result := agent.Runner{}.Run(context.Background(), agent.Spec{
		Adapter: opencode.New(), Runner: model.Runner{Name: "r", Agent: opencode.Name, ConfigDir: dir}, ConfigFolder: dir,
		Dir: dir, Prompt: "p", Env: map[string]string{"PATH": dir}, Sandbox: &agent.SandboxPlan{Work: dir},
	})

	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Contains(t, result.Note, `sandbox = "off"`)
	assert.NotContains(t, result.Note, "sandbox = None")
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

func TestReadUsage_IsUnknownWithTheReasonThatTheAgentReportsNone(t *testing.T) {
	t.Parallel()

	reading := opencode.New().ReadUsage(t.Context(), model.Runner{Name: "r", Agent: opencode.Name})

	_, known := reading.Usage()

	assert.False(t, known)
	assert.Equal(t, "the opencode agent cannot report usage", reading.Reason())
}

func TestTranscriptPath_NamesOneFileForEachSessionThatNoFileHolds(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	adapter := opencode.New()

	assert.Equal(t, filepath.Join(dir, "opencode", "session", "ses_1"), adapter.TranscriptPath(dir, "/work", "ses_1"))
	assert.NotEqual(t, adapter.TranscriptPath(dir, "/work", "ses_1"), adapter.TranscriptPath(dir, "/work", "ses_2"))
	assert.NoFileExists(t, adapter.TranscriptPath(dir, "/work", "ses_1"))
}

// answersVersion is the script lines of a fake opencode that prints version for --version and exits.
func answersVersion(version string) string {
	return "if [ \"$1\" = \"--version\" ]; then echo " + version + "; exit 0; fi\n"
}

// fakeEcho prints the arguments and then replays a fixture.
func fakeEcho(fixture string) string {
	return "#!/bin/sh\n" + answersVersion("1.19.5") + "echo \"args=$*\" >&2\ncat > /dev/null\ncat '" + fixture + "'\n"
}

func run(t *testing.T, change func(*agent.Spec)) (agent.Result, string) {
	t.Helper()

	dir := t.TempDir()
	fixture, err := filepath.Abs("testdata/ok.jsonl")
	require.NoError(t, err)
	require.NoError(t, exectest.Write(filepath.Join(dir, "opencode"), fakeEcho(fixture)))

	var out bytes.Buffer

	spec := agent.Spec{
		Adapter: opencode.New(), Runner: model.Runner{Name: "r", Agent: opencode.Name, ConfigDir: dir}, ConfigFolder: dir,
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

func TestRun_ARunOnVersionsFrom2_0_0OnUsesAPrivateServerAndOlderRunsDoNot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		version string
		private bool
	}{{"1.19.5", false}, {"1.99.99", false}, {"2.0.0", true}, {"2.2.5", true}, {"2.99.99", true}} {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()

			script := "#!/bin/sh\n" + answersVersion(tc.version) + "echo \"args=$*\" >&2\ncat > /dev/null\ncat '" + absolute(t, "testdata/ok-2x.jsonl") + "'\n"
			result := guardedRun(t, script, func(s *agent.Spec) { s.Guard = "" })

			assert.Equal(t, agent.OutcomeExited, result.Outcome, result.Note)
			assert.Equal(t, tc.private, strings.Contains(result.Stderr, "args=run --format json --standalone"), result.Stderr)
			assert.Equal(t, !tc.private, strings.Contains(result.Stderr, "args=run --format json\n"), result.Stderr)
		})
	}
}

func TestRun_TheRunnerSumsTheDollarsOfThe2xRecording(t *testing.T) {
	t.Parallel()

	script := "#!/bin/sh\n" + answersVersion("2.2.5") + "cat > /dev/null\ncat '" + absolute(t, "testdata/ok-2x.jsonl") + "'\n"
	result := guardedRun(t, script, func(s *agent.Spec) { s.Guard = ""; s.MaxBudget = 1 })

	assert.Equal(t, agent.OutcomeExited, result.Outcome, result.Note)
	assert.InDelta(t, 0.0020401, result.Streamed, 1e-12)
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
	script := "#!/bin/sh\n" + answersVersion(version) + "cat > /dev/null\n"
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
		Adapter: opencode.New(), Runner: model.Runner{Name: "r", Agent: opencode.Name, ConfigDir: dir}, ConfigFolder: dir,
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

func TestRun_ARunNeedsAnOpenCodeVersionFrom1_19_5UpToBut3_0_0OnEveryPath(t *testing.T) {
	t.Parallel()

	versions := []struct {
		version string
		starts  bool
	}{
		{"1.19.4", false}, {"1.19.5", true}, {"1.19.6", true}, {"1.99.99", true}, {"2.0.0", true}, {"2.2.5", true}, {"2.99.99", true}, {"3.0.0", false}, {"3.0.1", false},
	}

	for _, guard := range []string{"", "beehave check command"} {
		for _, tc := range versions {
			t.Run(tc.version+" guard="+guard, func(t *testing.T) {
				t.Parallel()

				result := guardedRun(t, guarded(absolute(t, "testdata/ok.jsonl"), tc.version, true), func(s *agent.Spec) { s.Guard = guard })

				if tc.starts {
					assert.Equal(t, agent.OutcomeExited, result.Outcome, result.Note)

					return
				}

				assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
				assert.Contains(t, result.Note, "opencode is at version "+tc.version)
				assert.Contains(t, result.Note, "It supports 1.19.5 up to but not including 3.0.0")
			})
		}
	}
}

func TestRun_AGuardedRunWhoseVersionCannotBeReadNamesTheRange(t *testing.T) {
	t.Parallel()

	result := guardedRun(t, guarded(absolute(t, "testdata/ok.jsonl"), "next", true), func(s *agent.Spec) {})

	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Contains(t, result.Note, "1.19.5 up to but not including 3.0.0")
	assert.NotContains(t, result.Note, "forbid_commands")
}

func TestRun_ARunWhoseVersionCannotBeReadDoesNotStart(t *testing.T) {
	t.Parallel()

	result := guardedRun(t, guarded(absolute(t, "testdata/ok.jsonl"), "next", true), func(s *agent.Spec) { s.Guard = "" })

	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Contains(t, result.Note, "1.19.5 up to but not including 3.0.0")
}

func TestRun_AGuardedRunOnWindowsDoesNotStart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	result := agent.Runner{OS: "windows"}.Run(context.Background(), agent.Spec{
		Adapter: opencode.New(), Runner: model.Runner{Name: "r", Agent: opencode.Name, ConfigDir: dir}, ConfigFolder: dir,
		Dir: dir, Prompt: "p", Env: map[string]string{"PATH": dir}, Guard: "g",
	})

	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Contains(t, result.Note, "does not start on Windows")
}

func TestRun_ARunThatSetsNoTimeoutEndsAtTheProfileLimitAndNamesTheLikelyCause(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(dir, "opencode"), "#!/bin/sh\n"+answersVersion("1.19.5")+"cat > /dev/null\nsleep 30\n"))

	var limit time.Duration

	result := agent.Runner{Version: func(context.Context, []string, []string) (string, error) { return "1.19.5", nil }, AfterFunc: func(d time.Duration, fire func()) agent.Timer {
		limit = d

		return time.AfterFunc(20*time.Millisecond, fire)
	}}.Run(context.Background(), agent.Spec{
		Adapter: opencode.New(), Runner: model.Runner{Name: "r", Agent: opencode.Name, ConfigDir: dir}, ConfigFolder: dir,
		Dir: dir, Prompt: "p", Env: map[string]string{"PATH": dir + ":/bin:/usr/bin"},
	})

	assert.Equal(t, time.Hour, limit)
	assert.Equal(t, agent.OutcomeTimedOut, result.Outcome)
	assert.Contains(t, result.Note, "usage quota")
}

func TestProfile_PinsTheRecordedVersionAndRefusesWhatItCannotProve(t *testing.T) {
	t.Parallel()

	p := opencode.New().Profile()

	assert.Equal(t, "1.19.5", p.SupportedFrom)
	assert.Equal(t, "3.0.0", p.SupportedBefore)
	assert.Empty(t, p.GuardBefore, "the guard plugin refuses a forbidden command on 1.x and on 2.x, so no version is closed to a guarded step")
	assert.True(t, p.GuardPosixOnly)
	assert.Equal(t, time.Hour, p.DefaultTimeout)
}

func TestProfile_OpenCodeHasNoSkillLine(t *testing.T) {
	t.Parallel()

	assert.Nil(t, opencode.New().Profile().SkillLine, "OpenCode has no line that loads a skill before the first turn, so a step with the skill keyword does not start on it")
}
