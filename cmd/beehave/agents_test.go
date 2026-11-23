package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/pool"
	"github.com/tobocop2/beehave/internal/proc"
)

// lineAdapter is an adapter whose output lines are "session=<id>" and "text=<message>".
type lineAdapter struct{ agent.Adapter }

func (lineAdapter) Guard(command, _ string, _, _ []string) agent.Launch {
	return agent.Launch{Args: []string{"--hook", command}, Hooked: true}
}

// Profile is the profile of a CLI that caps the dollars of a run itself.
func (lineAdapter) Profile() agent.Profile { return agent.Profile{BudgetFlag: "--max-budget-usd"} }

// noHookAdapter is an adapter whose CLI has no hook but whose guard launch still carries a setting, as the Claude Code launch carries the attribution setting.
type noHookAdapter struct{ lineAdapter }

func (noHookAdapter) Guard(string, string, []string, []string) agent.Launch {
	return agent.Launch{Args: []string{"--settings", `{"attribution":{"commit":"","pr":"","sessionUrl":false}}`}}
}

func (lineAdapter) Line(line string) (string, string) {
	if id, ok := cutPrefix(line, "session="); ok {
		return id, ""
	}

	if text, ok := cutPrefix(line, "text="); ok {
		return "", text
	}

	return "", ""
}

// Calls reads the lines "bash=<command>", "bash:<id>=<command>", "refused=<id>", "skill=<name>", "limit=<limit>" (see parseLimit), "denied=<reason>" and "result=end".
// A line that starts with "junk" is invalid.
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
		seen.Skills, seen.Succeeded = append(seen.Skills, agent.Call{ID: name, Text: name}), append(seen.Succeeded, name)
	}

	if kind, ok := cutPrefix(line, "stop="); ok {
		seen.Stop = agent.Stop(kind)
	}

	if subtype, ok := cutPrefix(line, "failed="); ok {
		seen.Failed = subtype
	}

	if limit, ok := cutPrefix(line, "limit="); ok {
		seen.Limit = parseLimit(limit)
	}

	if dollars, ok := cutPrefix(line, "cost="); ok {
		parsed, err := strconv.ParseFloat(dollars, 64)
		if err != nil {
			panic(err)
		}

		seen.Cost = parsed
	}

	seen.Result = line == "result=end"
	_, seen.Invalid = cutPrefix(line, "junk")
	seen.Denied, _ = cutPrefix(line, "denied=")

	return seen
}

// parseLimit reads "<kind>/<window>" or "<kind>/<window>@<unix seconds>".
func parseLimit(text string) *agent.Limit {
	kind, rest, _ := strings.Cut(text, "/")
	window, unix, timed := strings.Cut(rest, "@")
	limit := &agent.Limit{Kind: agent.LimitKind(kind), Window: window}

	if timed {
		seconds, err := strconv.ParseInt(unix, 10, 64)
		if err != nil {
			panic(err)
		}

		limit.ResetsAt = time.Unix(seconds, 0).UTC()
	}

	return limit
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
		modules: fakeModules{},
		run: func(ctx context.Context, spec agent.Spec) agent.Result {
			*f.spec = spec

			out := result
			if spec.BeforeStart != nil {
				out = out.AddNote(spec.BeforeStart(ctx))
			}

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

			return out
		},
		guard: "/bin/beehave check command",
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

func TestAgents_ACommitThatSetsAnAuthorIsAViolationOfTheKindGitAuthor(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bash = []string{"git commit -m x", "git commit --author='A <a@x>' -m x", "GIT_AUTHOR_NAME=me git commit -m y"}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingGitAuthor}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{"git-author: git commit", "git-author: GIT_AUTHOR_NAME"}, got.Violations)
	assert.Equal(t, "/bin/beehave check command --forbid git-author", f.spec.Guard)
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
	assert.Equal(t, "/bin/beehave check command --forbid bulk-staging,local-delete", f.spec.Guard)
}

func TestAgents_AProtectedDirectoryGoesToTheHookAsAnAbsolutePathFromTheFolderOfTheStep(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	req := request()
	req.Forbid = []model.FindingKind{model.FindingProtected}
	req.Protect = []string{"/own/it's", "../sibling"}

	_, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, `/bin/beehave check command --forbid protected-path --protect '/own/it'\''s' --protect /work/sibling`, f.spec.Guard)
}

func TestAgents_OnlyAPrivateProjectPassesItsVisibilityToTheHookAndACopyToAnotherMachineIsAViolationOtherwise(t *testing.T) {
	t.Parallel()

	for visibility, want := range map[model.Visibility]string{
		model.VisibilityPrivate: "/bin/beehave check command --forbid laptop-copy --visibility private",
		model.VisibilityPublic:  "/bin/beehave check command --forbid laptop-copy",
		"":                      "/bin/beehave check command --forbid laptop-copy",
	} {
		f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
		f.bash = []string{"rsync -a src host:dst"}
		f.agents.projects = map[string]config.Project{"p": {Project: model.Project{Visibility: visibility}}}
		req := request()
		req.Forbid = []model.FindingKind{model.FindingLaptopCopy}

		got, err := f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, want, f.spec.Guard)
		assert.Equal(t, visibility != model.VisibilityPrivate, len(got.Violations) == 1, "%q: %v", visibility, got.Violations)
	}
}

func TestAgents_ARequestWhoseProjectIsNotInTheConfigGetsNoPrivateVisibilityAndACopyIsAViolation(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bash = []string{"rsync -a src host:dst"}
	f.agents.projects = map[string]config.Project{"other": {Project: model.Project{Visibility: model.VisibilityPrivate}}}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingLaptopCopy}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, "/bin/beehave check command --forbid laptop-copy", f.spec.Guard)
	assert.Len(t, got.Violations, 1)
}

func TestAgents_AWriteInAProtectedDirectoryIsAViolationAndARelativeWriteInTheStepFolderIsNot(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.bash = []string{"touch /own/checkout/a", "git -C /own/checkout log", "touch notes.txt", "git -C /own/checkout commit -m x"}
	req := request()
	req.Forbid = []model.FindingKind{model.FindingProtected}
	req.Protect = []string{"/own/checkout"}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{"protected-path: touch /own/checkout/a", "protected-path: git -C /own/checkout commit"}, got.Violations)
}

func TestAgents_AStepThatForbidsCommandsDoesNotStartWithoutTheBinaryPathOfBeehave(t *testing.T) {
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

func TestAgents_AGuardThatDidNotLoadIsAViolationLine(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited, GuardMissing: true})
	req := request()
	req.Forbid = []model.FindingKind{model.FindingBulkStaging}

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{guardNotLoaded}, got.Violations)

	f = newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	got, err = f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Empty(t, got.Violations)
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

	assert.Equal(t, `'/tmp/it'\''s/beehave' check command`, guardCommand(func() (string, error) { return "/tmp/it's/beehave", nil }))
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

func TestAgents_TheCapsOfTheConfigGoToTheSpecOfEveryRun(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.maxTurns, f.agents.maxBudget = 12, 1.5

	_, err := f.agents.Run(t.Context(), request())

	require.NoError(t, err)
	assert.Equal(t, 12, f.spec.MaxTurns)
	assert.InDelta(t, 1.5, f.spec.MaxBudget, 0)
}

func TestAgents_TheCostOfTheResultLineIsTheCostOfTheRunWhetherOrNotACapStoppedIt(t *testing.T) {
	t.Parallel()

	for _, output := range []string{"session=s1\ncost=0.75\n", "session=s1\nstop=budget\ncost=2.5\n"} {
		f := newAgentFixture(output, agent.Result{Outcome: agent.OutcomeExited})

		got, err := f.agents.Run(t.Context(), request())

		require.NoError(t, err)
		assert.InDelta(t, map[bool]float64{true: 2.5, false: 0.75}[strings.Contains(output, "stop=")], got.Cost, 0)
	}
}

func TestAgents_TheStepCapGoesToTheSpecAndTheLowerOfTheStepCapAndTheGlobalCapWins(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ global, step, want float64 }{
		{0, 0, 0},
		{5, 0, 5},
		{0, 2, 2},
		{5, 2, 2},
		{1, 2, 1},
	} {
		f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
		f.agents.maxBudget = tc.global
		req := request()
		req.MaxBudget = tc.step

		_, err := f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.InDelta(t, tc.want, f.spec.MaxBudget, 0, "global %v, step %v", tc.global, tc.step)
	}
}

func TestAgents_ACapOnTheResultLineEndsTheRunAsACapWhateverTheExitCode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		stop string
		want agent.Outcome
	}{
		{"turns", agent.OutcomeTurnCap},
		{"budget", agent.OutcomeBudgetCap},
	} {
		for _, code := range []int{0, 1} {
			f := newAgentFixture("session=s1\ntext=halfway\nstop="+tc.stop+"\n", agent.Result{Outcome: agent.OutcomeExited, ExitCode: code})

			got, err := f.agents.Run(t.Context(), request())

			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Outcome, "exit code %d", code)
			assert.Equal(t, code, got.ExitCode)
			assert.Equal(t, "s1", got.Session)
			assert.Equal(t, "halfway", got.Text)
		}
	}
}

func TestAgents_ARunThatARateLimitStoppedIsLimitedNotFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code int
	}{
		{"exit_code_zero", 0},
		{"exit_code_one", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newAgentFixture("session=s1\ntext=halfway\nlimit=rate/five_hour@1795006800\nlimit=spend/seven_day\nfailed=success\n", agent.Result{Outcome: agent.OutcomeExited, ExitCode: tc.code})
			f.bash = []string{"git add -A"}
			req := request()
			req.Forbid = []model.FindingKind{model.FindingBulkStaging}

			got, err := f.agents.Run(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, agent.OutcomeLimited, got.Outcome)
			assert.Equal(t, tc.code, got.ExitCode)
			assert.Equal(t, &agent.Limit{Kind: agent.LimitRate, Window: "five_hour", ResetsAt: time.Unix(1795006800, 0).UTC()}, got.Limit, "the first limit of the output stopped the run")
			assert.Equal(t, "the provider refused more work at its rate limit five_hour until 2026-11-18T13:00:00Z", got.Note)
			assert.Equal(t, "s1", got.Session)
			assert.Equal(t, "halfway", got.Text)
			assert.Equal(t, []string{"bulk-staging: git add -A"}, got.Violations)
		})
	}
}

func TestAgents_ARunThatALimitStoppedBeforeItNamedASessionIsNotAnError(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("limit=spend/\n", agent.Result{Outcome: agent.OutcomeExited, ExitCode: 1})
	f.noResult = true

	got, err := f.agents.Run(t.Context(), request())

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeLimited, got.Outcome)
	assert.Equal(t, "the provider refused more work at its spend limit, and the agent gave no reset time", got.Note)
	assert.Empty(t, got.Session)
}

func TestAgents_ALimitedRunKeepsTheNoteOfTheRunnerAndAddsTheNoteOfTheLimit(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\nlimit=rate/five_hour\nfailed=success\n", agent.Result{Outcome: agent.OutcomeExited, ExitCode: 1, Note: "write the output: disk full"})

	got, err := f.agents.Run(t.Context(), request())

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeLimited, got.Outcome)
	assert.Equal(t, "write the output: disk full; the provider refused more work at its rate limit five_hour, and the agent gave no reset time", got.Note)
}

func TestAgents_ALimitThatTheRunGotPastIsNoLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, output string
		want         agent.Outcome
	}{
		{"success", "session=s1\nlimit=rate/five_hour\n", agent.OutcomeExited},
		{"cap", "session=s1\nlimit=rate/five_hour\nstop=turns\n", agent.OutcomeTurnCap},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newAgentFixture(tc.output, agent.Result{Outcome: agent.OutcomeExited})

			got, err := f.agents.Run(t.Context(), request())

			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Outcome)
			assert.Nil(t, got.Limit)
		})
	}
}

func TestAgents_ALimitOnARunThatDidNotExitKeepsItsOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result agent.Result
	}{
		{"timed_out", agent.Result{Outcome: agent.OutcomeTimedOut, ExitCode: -1}},
		{"canceled", agent.Result{Outcome: agent.OutcomeCancelled, ExitCode: -1}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newAgentFixture("session=s1\nlimit=rate/five_hour\nfailed=success\n", tc.result)

			got, err := f.agents.Run(t.Context(), request())

			require.NoError(t, err)
			assert.Equal(t, tc.result, got)
		})
	}
}

// claudeStream returns the lines of a fixture of the claude adapter at the given indexes, then the error result line that ends a refused run.
func claudeStream(t *testing.T, name string, indexes ...int) string {
	t.Helper()

	data, err := fs.ReadFile(os.DirFS(filepath.Join("..", "..", "internal", "agent", "claude", "testdata")), name)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")

	var stream strings.Builder
	for _, index := range indexes {
		stream.WriteString(lines[index] + "\n")
	}

	stream.WriteString(`{"type":"result","subtype":"success","is_error":true,"session_id":"0b6e2d8a-3c51-4f7e-a9d2-6e4b1c7f3a85"}` + "\n")

	return stream.String()
}

func TestAgents_AClaudeStreamOfARefusalThatNoWaitCuresEndsFailedWithTheReason(t *testing.T) {
	t.Parallel()

	const (
		credits = "the provider asks for usage credits, and no wait buys them: add usage credits or use another model"
		pause   = "the provider paused the usage of the organization, and no wait ends the pause: contact the support of the provider"
	)

	tests := []struct {
		name, fixture string
		indexes       []int
		want          string
	}{
		{"credits_event_then_credits_assistant_line", "rate-limited-credits-required.jsonl", []int{2, 1}, credits},
		{"pause_event_then_pause_assistant_line", "rate-limited-paused.jsonl", []int{0, 1}, pause},
	}

	for _, tc := range tests {
		t.Run(tc.name+"/exit_code_one", func(t *testing.T) {
			t.Parallel()

			f := newAgentFixture(claudeStream(t, tc.fixture, tc.indexes...), agent.Result{Outcome: agent.OutcomeExited, ExitCode: 1, Note: "write the output: disk full"})
			f.agents.adapter = func(model.Agent) agent.Adapter { return claude.Adapter{} }

			got, err := f.agents.Run(t.Context(), request())

			require.NoError(t, err)
			assert.Equal(t, agent.Result{Outcome: agent.OutcomeExited, ExitCode: 1, Note: "write the output: disk full; " + tc.want}, got, "the run fails with its exit code and waits for no reset")
		})

		t.Run(tc.name+"/exit_code_zero", func(t *testing.T) {
			t.Parallel()

			f := newAgentFixture(claudeStream(t, tc.fixture, tc.indexes...), agent.Result{Outcome: agent.OutcomeExited})
			f.agents.adapter = func(model.Agent) agent.Adapter { return claude.Adapter{} }

			got, err := f.agents.Run(t.Context(), request())

			require.ErrorIs(t, err, errDenied)
			require.ErrorContains(t, err, tc.want)
			assert.Equal(t, agent.Result{Outcome: agent.OutcomeExited, Note: tc.want}, got)
		})
	}
}

func TestAgents_ADeniedRunFailsAndWaitsForNoLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, output string
		noResult     bool
	}{
		{"a_limit_came_first", "session=s1\nlimit=rate/five_hour@1795006800\ndenied=paused\nfailed=success\n", false},
		{"the_first_reason_stays", "session=s1\ndenied=paused\ndenied=no credits\nfailed=success\n", false},
		{"no_result_line", "denied=paused\n", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newAgentFixture(tc.output, agent.Result{Outcome: agent.OutcomeExited})
			f.noResult = tc.noResult

			got, err := f.agents.Run(t.Context(), request())

			require.ErrorIs(t, err, errDenied)
			assert.Equal(t, agent.Result{Outcome: agent.OutcomeExited, Note: "paused"}, got)
		})
	}
}

func TestAgents_ADenialThatTheRunGotPastIsNoFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, output string
		want         agent.Outcome
	}{
		{"success", "session=s1\ndenied=paused\n", agent.OutcomeExited},
		{"cap", "session=s1\ndenied=paused\nstop=turns\n", agent.OutcomeTurnCap},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newAgentFixture(tc.output, agent.Result{Outcome: agent.OutcomeExited})

			got, err := f.agents.Run(t.Context(), request())

			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Outcome)
			assert.Empty(t, got.Note)
		})
	}
}

func TestAgents_ADenialOnARunThatDidNotExitKeepsItsOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result agent.Result
	}{
		{"timed_out", agent.Result{Outcome: agent.OutcomeTimedOut, ExitCode: -1}},
		{"canceled", agent.Result{Outcome: agent.OutcomeCancelled, ExitCode: -1}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newAgentFixture("session=s1\ndenied=paused\nfailed=success\n", tc.result)

			got, err := f.agents.Run(t.Context(), request())

			require.NoError(t, err)
			assert.Equal(t, tc.result, got)
		})
	}
}

func TestAgents_ACapOnARunThatTimedOutStaysATimeout(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\nstop=turns\n", agent.Result{Outcome: agent.OutcomeTimedOut, ExitCode: -1})

	got, err := f.agents.Run(t.Context(), request())

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeTimedOut, got.Outcome)
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

func TestAgents_AnErrorResultWithExitCodeZeroIsAFailureThatNamesTheSubtype(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\nfailed=error_during_execution\n", agent.Result{Outcome: agent.OutcomeExited})

	_, err := f.agents.Run(t.Context(), request())

	require.ErrorIs(t, err, errAgentFailed)
	require.ErrorContains(t, err, "error_during_execution")
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
	a := newAgents(cfg, flow.Options{Env: map[string]string{"PATH": "/bin"}}, treeAt(t.TempDir()), newAdapters(nil, nil, nil), nil)

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
		pool:     func(context.Context, []string) (string, error) { return "from-pool", nil },
	}

	login, err := picked.Pick(t.Context(), "p", "1", nil, false)
	require.NoError(t, err)

	assert.Equal(t, "from-pool", login)
}

func TestLogins_ThePickGivesTheBusyLoginsToThePool(t *testing.T) {
	t.Parallel()

	var got []string

	picked := logins{
		projects: map[string]config.Project{"p": {}},
		pool:     func(_ context.Context, busy []string) (string, error) { got = busy; return "x", nil },
	}

	_, err := picked.Pick(t.Context(), "p", "1", []string{"a", "b"}, false)
	require.NoError(t, err)

	assert.Equal(t, []string{"a", "b"}, got)
}

func TestPoolPick_PrefersAFreeClaudeRunner(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Runners: []model.Runner{{Name: "a", Agent: claude.Name}, {Name: "b", Agent: claude.Name}},
		Pool:    config.Pool{FiveHourMax: 90, SevenDayMax: 95},
	}
	read := func(_ context.Context, runner model.Runner) model.Reading {
		percent := 50.0
		if runner.Name == "b" {
			percent = 80
		}

		return model.Known(model.Usage{FiveHour: model.Window{Percent: percent}, SevenDay: model.Window{Percent: percent}})
	}

	got, err := poolPick(cfg, claude.Name, read, pool.Select)(t.Context(), []string{"a"})
	require.NoError(t, err)
	assert.Equal(t, "b", got, "b has more use than a, and a is busy")

	got, err = poolPick(cfg, claude.Name, read, pool.Select)(t.Context(), nil)
	require.NoError(t, err)
	assert.Equal(t, "a", got)
}

func TestPoolPick_AStepOfOneAgentKindNeverGetsARunnerOfAnotherKind(t *testing.T) {
	t.Parallel()

	other := model.Agent("other")
	cfg := config.Config{
		Runners: []model.Runner{{Name: "claude-1", Agent: claude.Name}, {Name: "other-1", Agent: other}},
		Pool:    config.Pool{FiveHourMax: 90, SevenDayMax: 95},
	}
	// Both kinds report usage, and the runner of the other kind has the lower use, so an unfiltered pick would take it.
	read := func(_ context.Context, runner model.Runner) model.Reading {
		percent := 80.0
		if runner.Agent == other {
			percent = 10
		}

		return model.Known(model.Usage{FiveHour: model.Window{Percent: percent}, SevenDay: model.Window{Percent: percent}})
	}

	for _, selectRunner := range []selector{pool.Select, pool.SelectOverCap} {
		got, err := poolPick(cfg, claude.Name, read, selectRunner)(t.Context(), nil)
		require.NoError(t, err)
		assert.Equal(t, "claude-1", got)

		got, err = poolPick(cfg, other, read, selectRunner)(t.Context(), nil)
		require.NoError(t, err)
		assert.Equal(t, "other-1", got)
	}
}

func TestPoolPick_AKindWithNoRunnerIsAnError(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Runners: []model.Runner{{Name: "other-1", Agent: model.Agent("other")}}}
	read := func(context.Context, model.Runner) model.Reading { return model.Known(model.Usage{}) }

	_, err := poolPick(cfg, claude.Name, read, pool.Select)(t.Context(), nil)

	require.ErrorIs(t, err, pool.ErrNoRunner)
}

func TestReadUsage_ARunnerOfAnAgentWithNoEntryIsUnknownSoThePoolNeverPicksIt(t *testing.T) {
	t.Parallel()

	reading := readUsage(agentEntries(nil))(t.Context(), model.Runner{Name: "o", Agent: model.Agent("none")})

	_, known := reading.Usage()

	assert.False(t, known)
	assert.Equal(t, "the none agent cannot report usage", reading.Reason())
}

func TestReadUsage_ARunnerOfARegisteredAgentIsReadThroughItsAdapter(t *testing.T) {
	t.Parallel()

	reading := readUsage(agentEntries(nil))(t.Context(), model.Runner{Name: "c", Agent: claude.Name})

	_, known := reading.Usage()

	assert.False(t, known)
	assert.Equal(t, "the runner has no config_dir, so its login is not known", reading.Reason())
}

func TestLogins_AnOverCapPickTakesTheLowestUseWhateverTheCaps(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Projects: map[string]config.Project{"p": {}},
		Runners:  []model.Runner{{Name: "a", Agent: claude.Name}, {Name: "b", Agent: claude.Name}},
		Pool:     config.Pool{FiveHourMax: 90, SevenDayMax: 95},
	}
	read := func(_ context.Context, runner model.Runner) model.Reading {
		percent := 99.0
		if runner.Name == "b" {
			percent = 97
		}

		return model.Known(model.Usage{FiveHour: model.Window{Percent: percent}, SevenDay: model.Window{Percent: percent}})
	}
	picked := loginsReading(cfg, read)

	_, err := picked.Pick(t.Context(), "p", "1", nil, false)
	var none *pool.NoneQualifiesError
	require.ErrorAs(t, err, &none)

	got, err := picked.Pick(t.Context(), "p", "1", nil, true)
	require.NoError(t, err)
	assert.Equal(t, "b", got)
}

func TestLogins_APoolFailureEndsThePick(t *testing.T) {
	t.Parallel()

	picked := logins{
		projects: map[string]config.Project{"p": {}},
		pool:     func(context.Context, []string) (string, error) { return "", errFake },
	}

	_, err := picked.Pick(t.Context(), "p", "1", nil, false)

	require.ErrorIs(t, err, errFake)
}

func TestPoolPick_ChoosesTheClaudeRunnerUnderTheCaps(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Pool: config.Pool{FiveHourMax: 90, SevenDayMax: 95},
		Runners: []model.Runner{
			{Name: "busy", Agent: claude.Name},
			{Name: "free", Agent: claude.Name},
		},
	}
	read := func(_ context.Context, runner model.Runner) model.Reading {
		percent := 10.0
		if runner.Name == "busy" {
			percent = 99
		}

		return model.Known(model.Usage{FiveHour: model.Window{Percent: percent}, SevenDay: model.Window{Percent: percent}})
	}

	got, err := poolPick(cfg, claude.Name, read, pool.Select)(t.Context(), nil)

	require.NoError(t, err)
	assert.Equal(t, "free", got)

	_, err = poolPick(config.Config{}, claude.Name, read, pool.Select)(t.Context(), nil)
	require.ErrorIs(t, err, pool.ErrNoRunner)
}

func TestNewLogins_AConfigWithNoRunnerHasNoPick(t *testing.T) {
	t.Parallel()

	_, err := newLogins(config.Config{}, flow.Options{}).pool(t.Context(), nil)

	require.ErrorIs(t, err, pool.ErrNoRunner)
}

// usageFixture is a config of one claude runner and options whose PATH holds a fake claude that exits 7 and says so. The reading of the runner
// then carries the words of the fake, so a pick that fails shows that the usage came from the claude adapter.
func usageFixture(t *testing.T) (config.Config, flow.Options) {
	t.Helper()

	tools := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(tools, "claude"), "#!/bin/sh\necho fake-claude-ran >&2\nexit 7\n"))

	cfg := config.Config{
		Pool:    config.Pool{FiveHourMax: 90, SevenDayMax: 95},
		Runners: []model.Runner{{Name: "a", Agent: claude.Name, ConfigDir: t.TempDir()}},
	}

	return cfg, flow.Options{Env: map[string]string{"PATH": tools, "HOME": t.TempDir()}}
}

func TestNewLogins_ThePickReadsTheUsageThroughTheClaudeAdapterOnThePathOfTheOptions(t *testing.T) {
	t.Parallel()

	cfg, opts := usageFixture(t)
	picks := newLogins(cfg, opts)

	_, err := picks.pool(t.Context(), nil)
	require.ErrorContains(t, err, "claude exited 7: fake-claude-ran")

	_, err = picks.overCap(t.Context(), nil)
	require.ErrorContains(t, err, "claude exited 7: fake-claude-ran")
}

func TestNewDeps_TheEnginePickReadsTheUsageThroughTheClaudeAdapterOnThePathOfTheOptions(t *testing.T) {
	t.Parallel()

	cfg, opts := usageFixture(t)
	cfg.Projects = map[string]config.Project{"p": {}}

	_, err := newDeps(cfg, opts, newAdapters(proc.NewExecRunner("bd", ""), stateFolder(opts.StateDir), opts.Env)).Logins.Pick(t.Context(), "p", "1", nil, false)

	require.ErrorContains(t, err, "claude exited 7: fake-claude-ran")
}

func TestAgents_TheSpecOfAStepCarriesItsEditsSwitch(t *testing.T) {
	t.Parallel()

	for _, noEdits := range []bool{false, true} {
		spec := agents{}.specOf(flow.AgentRequest{NoEdits: noEdits}, model.Runner{}, "", nil, nil, "", nil, nil)

		assert.Equal(t, noEdits, spec.NoEdits, "a step with edits = False starts without the edit mode")
	}
}

func TestAgents_TheSpecOfAStepCarriesTheRedactorOfTheEngine(t *testing.T) {
	t.Parallel()

	request := flow.AgentRequest{Redact: agent.Redactor("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")}
	spec := agents{}.specOf(request, model.Runner{}, "", nil, nil, "", nil, nil)

	require.NotNil(t, spec.Redact)
	assert.Equal(t, "key [redacted]", spec.Redact("key wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"))
}

func TestAgents_ARunThatReachedTheBudgetCapKeepsItsSessionTextAndViolations(t *testing.T) {
	t.Parallel()

	for _, code := range []int{0, -1} {
		f := newAgentFixture("session=s1\ntext=halfway\n", agent.Result{Outcome: agent.OutcomeBudgetCap, ExitCode: code, Note: "no cost"})
		f.bash = []string{"git add -A"}
		req := request()
		req.Forbid = []model.FindingKind{model.FindingBulkStaging}

		got, err := f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeBudgetCap, got.Outcome, "exit code %d", code)
		assert.Equal(t, "no cost", got.Note)
		assert.Equal(t, "s1", got.Session)
		assert.Equal(t, "halfway", got.Text)
		assert.Equal(t, []string{"bulk-staging: git add -A"}, got.Violations)
	}
}

func TestAgents_ARunThatReachedTheBudgetCapBeforeItNamedASessionIsNotAnError(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("", agent.Result{Outcome: agent.OutcomeBudgetCap, ExitCode: -1})

	got, err := f.agents.Run(t.Context(), request())

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeBudgetCap, got.Outcome)
	assert.Empty(t, got.Session)
}
