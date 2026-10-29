package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/pool"
)

// lineAdapter is an adapter whose output lines are "session=<id>" and "text=<message>".
type lineAdapter struct{ agent.Adapter }

func (lineAdapter) Guard(command string) []string { return []string{"--hook", command} }

// noHookAdapter is an adapter whose CLI has no hook.
type noHookAdapter struct{ lineAdapter }

func (noHookAdapter) Guard(string) []string { return nil }

func (lineAdapter) Line(line string) (string, string) {
	if id, ok := cutPrefix(line, "session="); ok {
		return id, ""
	}

	if text, ok := cutPrefix(line, "text="); ok {
		return "", text
	}

	return "", ""
}

// Calls reads the lines "bash=<command>", "bash:<id>=<command>", "refused=<id>", "skill=<name>" and "result=end". A line that starts with "junk" is invalid.
func (lineAdapter) Calls(line string) agent.Seen {
	var seen agent.Seen

	if command, ok := cutPrefix(line, "bash="); ok {
		seen.Shell = append(seen.Shell, agent.Call{Text: command})
	}

	if rest, ok := cutPrefix(line, "bash:"); ok {
		id, command, _ := strings.Cut(rest, "=")
		seen.Shell = append(seen.Shell, agent.Call{ID: id, Text: command})
	}

	if id, ok := cutPrefix(line, "refused="); ok {
		seen.Refused = append(seen.Refused, id)
	}

	if name, ok := cutPrefix(line, "skill="); ok {
		seen.Skills = append(seen.Skills, name)
	}

	seen.Result = line == "result=end"
	_, seen.Invalid = cutPrefix(line, "junk")

	return seen
}

func (lineAdapter) TranscriptPath(configDir, dir, session string) string {
	return configDir + "|" + dir + "|" + session
}

func cutPrefix(line, prefix string) (string, bool) {
	if len(line) < len(prefix) || line[:len(prefix)] != prefix {
		return "", false
	}

	return line[len(prefix):], true
}

type errTrees struct{}

func (errTrees) Path(context.Context, string, string) (string, error) { return "", errFake }

// agentFixture is an agents value over fakes. The fake run records the spec and writes the lines in output to the log of the spec.
type agentFixture struct {
	agents agents
	spec   *agent.Spec
	kind   model.Agent
	// bash holds the Bash calls that the fake run prints as lines of its output.
	bash []string
	// extra holds more lines that the fake run prints after the calls. noResult leaves out the result line that ends the run.
	extra    []string
	noResult bool
}

func newAgentFixture(output string, result agent.Result) *agentFixture {
	f := &agentFixture{spec: &agent.Spec{}}

	f.agents = agents{
		runners: []model.Runner{{Name: "main", Agent: model.Agent("fake"), ConfigDir: "/login"}},
		trees:   treeAt("/work/tree"),
		adapter: func(kind model.Agent) agent.Adapter { f.kind = kind; return lineAdapter{} },
		env:     map[string]string{"PATH": "/bin"},
		run: func(_ context.Context, spec agent.Spec) agent.Result {
			*f.spec = spec
			var lines strings.Builder

			lines.WriteString(output + "skill=blast\nskill=oracle\n")

			for _, call := range f.bash {
				lines.WriteString("bash=" + call + "\n")
			}

			for _, extra := range f.extra {
				lines.WriteString(extra + "\n")
			}

			if !f.noResult {
				lines.WriteString("result=end\n")
			}

			if _, err := spec.Log.Write([]byte(lines.String())); err != nil {
				panic(err)
			}

			return result
		},
		guard: "'/bin/obee' check command",
	}

	return f
}

func TestAgents_ABashCallOfAListedKindIsAViolationAndTheRunStillSucceeds(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bash = []string{"git add -A", "echo   hi", "rm -rf $dir"}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging, model.FindingLocalDelete}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Equal(t, []string{"bulk-staging: git add -A", "local-delete: rm -rf $dir"}, got.Violations)
}

func TestAgents_ACleanTranscriptHasNoViolations(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bash = []string{"go test ./..."}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Empty(t, got.Violations)
}

func TestAgents_ABashCallOfAKindThatIsNotListedIsNotRecorded(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bash = []string{"git add -A"}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingRemoteDelete}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Empty(t, got.Violations)
}

func TestAgents_AStepThatForbidsNothingRecordsNoViolationAndInstallsNoHook(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bash = []string{"git add -A"}

	got, err := f.agents.Run(t.Context(), request())

	require.NoError(t, err)
	assert.Empty(t, got.Violations)
	assert.Empty(t, f.spec.Guard)
}

func TestAgents_AStepThatForbidsKindsRunsTheAgentWithTheHookOfThoseKinds(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging, model.FindingLocalDelete}

	_, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, "'/bin/obee' check command --forbid bulk-staging,local-delete", f.spec.Guard)
}

func TestAgents_AStepThatForbidsCommandsDoesNotStartWithoutTheBinaryPathOfObee(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.guard = ""
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "forbid_commands")
	assert.Contains(t, got.Note, "path of its own program")
	assert.Empty(t, f.spec.Dir, "the agent never ran")
}

func TestAgents_AStepThatForbidsCommandsDoesNotStartOnAnAgentWithNoHook(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.adapter = func(model.Agent) agent.Adapter { return noHookAdapter{lineAdapter{}} }
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "no hook")
	assert.Empty(t, f.spec.Dir, "the agent never ran")

	req.Forbid = nil
	got, err = f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeExited, got.Outcome, "a step that forbids nothing needs no hook")
}

func TestAgents_ACallThatTheHookRefusedIsNotAViolation(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.extra = []string{"bash:t1=git add -A", "refused=t1", "bash:t2=git commit -a -m x", "bash:t3=git add -A", "refused=other"}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{"bulk-staging: git commit -a", "bulk-staging: git add -A"}, got.Violations, "t1 never ran; t2 and t3 did")
}

func TestAgents_ALineThatTheRunnerDroppedIsAViolationLine(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.extra = []string{agent.DroppedLine}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{"a line of the output was too long to check"}, got.Violations)
}

func TestAgents_AnOutputWithNoResultLineIsAViolationLine(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.noResult = true
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{"the output of the run was not complete: it has no result line"}, got.Violations)

	req.Forbid = nil
	got, err = f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Empty(t, got.Violations, "a step that forbids nothing records no violation")
}

func TestAgents_AnOutputWithALineThatIsNotJSONIsAViolationLine(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.extra = []string{"junk"}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{"the output of the run was not complete: it holds a line that is not valid JSON"}, got.Violations)
}

func TestGuardCommand_QuotesThePathOfTheBinary(t *testing.T) {
	t.Parallel()

	assert.Equal(t, `'/tmp/it'\''s/obee' check command`, guardCommand(func() (string, error) { return "/tmp/it's/obee", nil }))
	assert.Empty(t, guardCommand(func() (string, error) { return "", errFake }))
}

func TestAgents_AHitIsOneLineWithTheCommandCollapsedAndCut(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bash = []string{"git   add\t-A " + strings.Repeat("x", 300)}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	require.Len(t, got.Violations, 1)
	assert.NotContains(t, got.Violations[0], "\n")
	assert.True(t, strings.HasPrefix(got.Violations[0], "bulk-staging: git add -A"))
	assert.LessOrEqual(t, len(got.Violations[0]), len("bulk-staging: ")+160)
}

func TestAgents_TheLinesOfViolationsAreBoundedAndOneLineSaysHowManyAreLeftOut(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})

	for range 25 {
		f.bash = append(f.bash, "git add -A")
	}

	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	require.Len(t, got.Violations, maxViolationLines+1)
	assert.Equal(t, "bulk-staging: git add -A", got.Violations[maxViolationLines-1])
	assert.Equal(t, "5 more violations are not shown", got.Violations[maxViolationLines])
}

func TestAgents_TheIncompleteLinesCountInTheCapSoTheOutputNeverExceedsTheCapAndOneLine(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.noResult = true

	for range 25 {
		f.bash = append(f.bash, "git add -A")
	}

	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	require.Len(t, got.Violations, maxViolationLines+1)
	assert.Equal(t, "6 more violations are not shown", got.Violations[maxViolationLines-1])
	assert.Equal(t, noResultLine, got.Violations[maxViolationLines])
}

func TestAgents_OneViolationLeftOutIsCountedInTheSingular(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})

	for range maxViolationLines + 1 {
		f.bash = append(f.bash, "git add -A")
	}

	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	require.Len(t, got.Violations, maxViolationLines+1)
	assert.Equal(t, "1 more violation is not shown", got.Violations[maxViolationLines])
}

func TestAgents_ExactlyTheMostViolationLinesHaveNoCountLine(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})

	for range maxViolationLines {
		f.bash = append(f.bash, "git add -A")
	}

	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Len(t, got.Violations, maxViolationLines)
}

func request() flow.AgentRequest {
	return flow.AgentRequest{
		Item: "1", Project: "p", Login: "main", Prompt: "do it", Resume: "old", Model: "opus",
		Timeout: time.Minute, IdleTimeout: time.Second,
	}
}

func TestAgents_RunTheRunnerOfTheLoginInTheWorktreeAndReadTheSession(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("noise\nsession=s1\ntext=first\r\nsession=s2\ntext=last\n", agent.Result{Outcome: agent.OutcomeExited})
	started := 0
	req := request()
	req.Started = func(int) { started++ }

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.Result{
		Outcome: agent.OutcomeExited, Session: "s2", Text: "last", Transcript: "/login|/work/tree|s2", Skills: []string{"blast", "oracle"},
	}, got)
	assert.Equal(t, "/work/tree", f.spec.Dir)
	assert.Equal(t, "do it", f.spec.Prompt)
	assert.Equal(t, "opus", f.spec.Model)
	assert.Equal(t, "old", f.spec.Resume)
	assert.Equal(t, time.Minute, f.spec.Timeout)
	assert.Equal(t, time.Second, f.spec.IdleTimeout)
	assert.Equal(t, map[string]string{"PATH": "/bin"}, f.spec.Env)
	assert.Equal(t, model.Runner{Name: "main", Agent: model.Agent("fake"), ConfigDir: "/login"}, f.spec.Runner)
	assert.Equal(t, model.Agent("fake"), f.kind, "the adapter is the one the runner names")

	f.spec.OnStart(1)
	assert.Equal(t, 1, started)
}

func TestAgents_ARunThatDidNotExitCleanlyIsReturnedWithoutASession(t *testing.T) {
	t.Parallel()

	for _, result := range []agent.Result{
		{Outcome: agent.OutcomeTimedOut, ExitCode: -1},
		{Outcome: agent.OutcomeNotStarted, Note: "claude is not installed"},
		{Outcome: agent.OutcomeExited, ExitCode: 3},
	} {
		f := newAgentFixture("session=s1\n", result)

		got, err := f.agents.Run(t.Context(), request())

		require.NoError(t, err)
		assert.Equal(t, result, got)
	}
}

func TestAgents_ACleanRunWithNoSessionIdIsAnError(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("text=hello\n", agent.Result{Outcome: agent.OutcomeExited})

	_, err := f.agents.Run(t.Context(), request())

	require.ErrorIs(t, err, errNoSession)
}

func TestAgents_RefuseARequestThatCannotRun(t *testing.T) {
	t.Parallel()

	empty := newAgentFixture("", agent.Result{})
	empty.agents.trees = treeAt("")
	empty.agents.run = func(context.Context, agent.Spec) agent.Result {
		panic("the agent must not start")
	}
	broken := *empty
	broken.agents.trees = errTrees{}

	noPrompt := request()
	noPrompt.Prompt = ""
	noRunner := request()
	noRunner.Login = "other"

	tests := []struct {
		name    string
		agents  agents
		request flow.AgentRequest
		want    error
		text    string
	}{
		{"no prompt", empty.agents, noPrompt, errNoPrompt, "item 1"},
		{"unknown login", empty.agents, noRunner, errNoRunner, `"other"`},
		{"no worktree", empty.agents, request(), errNoFolder, "item 1 of project p"},
		{"trees fail", broken.agents, request(), errFake, "item 1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := test.agents.Run(t.Context(), test.request)

			require.ErrorIs(t, err, test.want)
			assert.Contains(t, err.Error(), test.text)
		})
	}
}

func TestNewAgents_WiresTheRealRunnerAndTheAdapterTable(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Runners: []model.Runner{{Name: "x", Agent: model.Agent("none"), ConfigDir: "/login"}}}
	a := newAgents(cfg, flow.Options{Env: map[string]string{"PATH": "/bin"}}, treeAt(t.TempDir()), newAdapters(nil, nil))

	got, err := a.Run(t.Context(), flow.AgentRequest{Item: "1", Project: "p", Login: "x", Prompt: "go"})

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "cannot be started yet")
	assert.Contains(t, a.guard, " check command")
}

func TestLogins_AKnownProjectTakesThePoolPick(t *testing.T) {
	t.Parallel()

	picked := logins{
		projects: map[string]config.Project{"p": {}},
		pool:     func(context.Context) (string, error) { return "from-pool", nil },
	}

	login, err := picked.Pick(t.Context(), "p", "1")
	require.NoError(t, err)

	assert.Equal(t, "from-pool", login)
}

func TestLogins_APoolFailureEndsThePick(t *testing.T) {
	t.Parallel()

	picked := logins{
		projects: map[string]config.Project{"p": {}},
		pool:     func(context.Context) (string, error) { return "", errFake },
	}

	_, err := picked.Pick(t.Context(), "p", "1")

	require.ErrorIs(t, err, errFake)
}

func TestPoolPick_ChoosesTheClaudeRunnerUnderTheCaps(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Pool: config.Pool{FiveHourMax: 90, SevenDayMax: 95},
		Runners: []model.Runner{
			{Name: "busy", Agent: model.AgentClaude},
			{Name: "free", Agent: model.AgentClaude},
		},
	}
	read := func(_ context.Context, runner model.Runner) model.Reading {
		percent := 10.0
		if runner.Name == "busy" {
			percent = 99
		}

		return model.Known(model.Usage{FiveHour: model.Window{Percent: percent}, SevenDay: model.Window{Percent: percent}})
	}

	got, err := poolPick(cfg, read)(t.Context())

	require.NoError(t, err)
	assert.Equal(t, "free", got)

	_, err = poolPick(config.Config{}, read)(t.Context())
	require.ErrorIs(t, err, pool.ErrNoRunner)
}

func TestNewLogins_ReadsTheUsageOfTheClaudeRunners(t *testing.T) {
	t.Parallel()

	_, err := newLogins(config.Config{}, flow.Options{}).pool(t.Context())

	require.ErrorIs(t, err, pool.ErrNoRunner)
}
