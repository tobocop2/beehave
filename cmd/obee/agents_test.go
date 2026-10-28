package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/agent/claude"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/pool"
)

// lineAdapter is an adapter whose output lines are "session=<id>" and "text=<message>".
type lineAdapter struct{ agent.Adapter }

func (lineAdapter) Line(line string) (string, string) {
	if id, ok := cutPrefix(line, "session="); ok {
		return id, ""
	}

	if text, ok := cutPrefix(line, "text="); ok {
		return "", text
	}

	return "", ""
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
	// bash holds the Bash calls that the fake transcript shows. bashReads counts the reads of them.
	bash      []model.Call
	bashErr   error
	bashReads int
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
			if _, err := spec.Log.Write([]byte(output)); err != nil {
				panic(err)
			}

			return result
		},
		skills: func(agent.Adapter, string) ([]model.Call, error) {
			return []model.Call{{Line: 1, Text: "blast"}, {Line: 4, Text: "oracle"}}, nil
		},
		bash: func(agent.Adapter, string) ([]model.Call, error) {
			f.bashReads++

			return f.bash, f.bashErr
		},
	}

	return f
}

func TestAgents_ABashCallOfAListedKindIsAViolationAndTheRunStillSucceeds(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bash = []model.Call{{Line: 3, Text: "git add -A"}, {Line: 5, Text: "echo   hi"}, {Line: 8, Text: "rm -rf $dir"}}
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
	f.bash = []model.Call{{Line: 3, Text: "go test ./..."}}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Empty(t, got.Violations)
	assert.Equal(t, 1, f.bashReads)
}

func TestAgents_ABashCallOfAKindThatIsNotListedIsNotRecorded(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bash = []model.Call{{Line: 3, Text: "git add -A"}}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingRemoteDelete}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Empty(t, got.Violations)
}

func TestAgents_AStepThatForbidsNothingNeverReadsTheBashCalls(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bashErr = errFake

	got, err := f.agents.Run(t.Context(), request())

	require.NoError(t, err)
	assert.Empty(t, got.Violations)
	assert.Zero(t, f.bashReads)
}

func TestAgents_AnUnreadableTranscriptFailsAStepThatForbidsCommands(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bashErr = errFake
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	_, err := f.agents.Run(t.Context(), req)

	require.ErrorIs(t, err, errFake)
	assert.ErrorContains(t, err, "read the Bash calls of session s1")
}

func TestAgents_AHitIsOneLineWithTheCommandCollapsedAndCut(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bash = []model.Call{{Line: 1, Text: "git   add\t-A " + strings.Repeat("x", 300)}}
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

	for i := range 25 {
		f.bash = append(f.bash, model.Call{Line: i + 1, Text: "git add -A"})
	}

	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	require.Len(t, got.Violations, maxViolationLines+1)
	assert.Equal(t, "bulk-staging: git add -A", got.Violations[maxViolationLines-1])
	assert.Equal(t, "5 more violations are not shown", got.Violations[maxViolationLines])
}

func TestAgents_OneViolationLeftOutIsCountedInTheSingular(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})

	for i := range maxViolationLines + 1 {
		f.bash = append(f.bash, model.Call{Line: i + 1, Text: "git add -A"})
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

	for i := range maxViolationLines {
		f.bash = append(f.bash, model.Call{Line: i + 1, Text: "git add -A"})
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

func TestAgents_AnUnreadableTranscriptIsAnErrorNamingTheSession(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.skills = func(agent.Adapter, string) ([]model.Call, error) { return nil, errFake }

	_, err := f.agents.Run(t.Context(), request())

	require.ErrorIs(t, err, errFake)
	assert.Contains(t, err.Error(), "session s1")
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

	_, err = a.skills(claude.New(), t.TempDir()+"/none.jsonl")
	require.ErrorIs(t, err, agent.ErrUnreadable)

	_, err = a.bash(claude.New(), t.TempDir()+"/none.jsonl")
	require.ErrorIs(t, err, agent.ErrUnreadable)
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
