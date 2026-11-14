package agent_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/agent/opencode"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
)

const token = "tok-secret-value"

type fixture struct {
	records string
	config  string
	work    string
	log     *bytes.Buffer
	spec    agent.Spec
}

// newFixture installs a fake agent in the given mode and returns a spec that starts it in a temp HOME.
func newFixture(t *testing.T, name string, kind model.Agent, mode string) *fixture {
	t.Helper()

	records := install(t, name, mode)
	home := t.TempDir()
	config := filepath.Join(home, "login")
	require.NoError(t, os.Mkdir(config, 0o700))

	work, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	log := &bytes.Buffer{}

	var adapter agent.Adapter
	if kind == claude.Name {
		adapter = claude.New(nil)
	}

	return &fixture{records: records, config: config, work: work, log: log, spec: agent.Spec{
		Adapter: adapter,
		Runner:  model.Runner{Name: "main", Agent: kind, ConfigDir: config},
		Dir:     work,
		Prompt:  "do the step\n",
		Env: map[string]string{
			"PATH": records + ":/bin:/usr/bin", "HOME": home, "LC_ALL": "C",
			"ANTHROPIC_API_KEY": "leak", "GH_TOKEN": "leak", "CLAUDE_CONFIG_DIR": "/other/login",
		},
		Token: token,
		Log:   log,
	}}
}

func TestRunner_StartsWithTheAllowlistedEnvironment(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "")

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.Result{Outcome: agent.OutcomeExited}, got)
	assert.Equal(t, "-p --output-format stream-json --verbose --allowedTools Bash --permission-mode acceptEdits", record(t, f.records, "args"))
	assert.Equal(t, "do the step", record(t, f.records, "stdin"))
	assert.Equal(t, f.work, record(t, f.records, "cwd"))

	env := record(t, f.records, "env")
	assert.Contains(t, env, "CLAUDE_CONFIG_DIR="+f.config+"\n")
	assert.Contains(t, env, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+f.config+"\n")
	assert.Contains(t, env, "CLAUDE_CODE_OAUTH_TOKEN="+token+"\n")
	assert.Contains(t, env, "LC_ALL=C")
	assert.NotContains(t, env, "ANTHROPIC_API_KEY")
	assert.NotContains(t, env, "GH_TOKEN")
	assert.NotContains(t, env, "/other/login")
}

func TestRunner_PassesTheModelAndTheSessionToResumeAsFlags(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "")
	f.spec.Model, f.spec.Resume = "opus", "sess-1"

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Equal(t, "-p --output-format stream-json --verbose --allowedTools Bash --permission-mode acceptEdits --model opus --resume sess-1", record(t, f.records, "args"))
}

func TestRunner_AGuardAddsTheHookArgumentsOfTheAdapterAndNoGuardAddsNone(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "")
	f.spec.Guard = "beehave check command"

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Contains(t, record(t, f.records, "args"), `--settings {"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"beehave check command","onFailure":"block"}]}]}}`)

	f.spec.Guard = ""
	agent.Runner{}.Run(context.Background(), f.spec)

	assert.NotContains(t, record(t, f.records, "args"), "--settings")
}

func TestRunner_RefusesAModelOrAResumeThatTheCLIHasNoFlagFor(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "")
	f.spec.Adapter = noFlags{f.spec.Adapter}

	f.spec.Model = "opus"
	named := agent.Runner{}.Run(context.Background(), f.spec)

	f.spec.Model, f.spec.Resume = "", "sess-1"
	resume := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, named.Outcome)
	assert.Contains(t, named.Note, `no option to set a model, so model "opus" is not used`)
	assert.Equal(t, agent.OutcomeNotStarted, resume.Outcome)
	assert.Contains(t, resume.Note, "no option to resume a session, so the run does not start. Remove resume from the step.")
	assert.Contains(t, named.Note, "Remove the model from the step.")
}

func TestRunner_PassesTheTurnAndBudgetCapsAsFlagsAndNoCapAddsNone(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "")
	f.spec.MaxTurns, f.spec.MaxBudget = 40, 2.5

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Equal(t, "-p --output-format stream-json --verbose --allowedTools Bash --permission-mode acceptEdits --max-turns 40 --max-budget-usd 2.5", record(t, f.records, "args"))

	f.spec.MaxTurns, f.spec.MaxBudget = 0, 0
	agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, "-p --output-format stream-json --verbose --allowedTools Bash --permission-mode acceptEdits", record(t, f.records, "args"))
}

func TestRunner_AStepWithNoEditsStartsWithoutThePermissionMode(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "")
	f.spec.NoEdits = true

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Equal(t, "-p --output-format stream-json --verbose --allowedTools Bash", record(t, f.records, "args"))
	assert.NotContains(t, record(t, f.records, "args"), "--permission-mode")
}

func TestRunner_RefusesACapThatTheCLIHasNoFlagFor(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "")
	f.spec.Adapter = noFlags{f.spec.Adapter}

	f.spec.MaxBudget = 1
	budget := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, budget.Outcome)
	assert.Contains(t, budget.Note, "no option to cap the cost, so the run does not start. Set agents.max_budget_usd to 0.")
}

// noFlags is an adapter whose CLI has no model, resume or cap option.
type noFlags struct{ agent.Adapter }

func (n noFlags) Profile() agent.Profile {
	p := n.Adapter.Profile()
	p.ModelFlag, p.Resume, p.TurnsFlag, p.BudgetFlag = "", nil, "", ""

	return p
}

func TestRunner_FindsTheBinaryOnTheSpecPathNotTheParentPath(t *testing.T) {
	f := newFixture(t, "claude", claude.Name, "")
	t.Setenv("PATH", "/bin:/usr/bin")

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.Result{Outcome: agent.OutcomeExited}, got)
	assert.Equal(t, "do the step", record(t, f.records, "stdin"))
}

func TestRunner_RedactsSecretsInTheLogAndTheStderrTail(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "secret")

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, 3, got.ExitCode)
	assert.Equal(t, "err [redacted]\n", got.Stderr)
	assert.Equal(t, "token is [redacted] and [redacted] here\n", f.log.String())
	assert.NotContains(t, got.Stderr, token)
}

func TestRunner_ReportsThePidOfTheAgentToOnStartBeforeItEnds(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "fail")

	var pids []int

	f.spec.OnStart = func(pid int) { pids = append(pids, pid) }

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	require.Len(t, pids, 1)
	assert.Positive(t, pids[0])
}

func TestRunner_ReportsTheExitCodeAndTheStderrTail(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "fail")

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.Result{Outcome: agent.OutcomeExited, ExitCode: 7, Stderr: "boom\n"}, got)
}

func TestRunner_WritesTheLastLineThatHasNoNewline(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "partial")

	agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, "no newline at the end", f.log.String())
}

func TestRunner_AFailingLogEndsTheRunWithTheCause(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "partial")
	f.spec.Log = failingWriter{}

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Contains(t, got.Note, "disk full")
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestRunner_StopsOnTheTotalTimeout(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "silent")
	f.spec.Timeout = 300 * time.Millisecond

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeTimedOut, got.Outcome)
}

func TestRunner_TheTotalTimeoutKillsTheAgentAndItsChildren(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "holdgrand")
	f.spec.Timeout = time.Hour
	clock := &fakeClock{}

	done := make(chan agent.Result, 1)

	go func() { done <- agent.Runner{AfterFunc: clock.AfterFunc}.Run(context.Background(), f.spec) }()

	require.Eventually(t, func() bool { return fileExists(filepath.Join(f.records, "grandchild")) }, 30*time.Second, 10*time.Millisecond,
		"the agent started its child")
	clock.fireNow()

	got := <-done

	assert.Equal(t, time.Hour, clock.limit)
	assert.Equal(t, agent.OutcomeTimedOut, got.Outcome)
	assertGone(t, f.records)
}

// fakeClock stands in for the idle timer: the test decides when it fires.
type fakeClock struct {
	mu     sync.Mutex
	fire   func()
	limit  time.Duration
	resets int
	stops  int
}

func (c *fakeClock) AfterFunc(limit time.Duration, fire func()) agent.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.limit, c.fire = limit, fire

	return c
}

func (c *fakeClock) Reset(time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.resets++

	return true
}

func (c *fakeClock) Stop() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stops++

	return true
}

func (c *fakeClock) fireNow() {
	c.mu.Lock()
	fire := c.fire
	c.mu.Unlock()

	fire()
}

func (c *fakeClock) resetCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.resets
}

func TestRunner_StopsWhenTheIdleTimerFires(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "silent")
	f.spec.IdleTimeout = 7 * time.Second
	clock := &fakeClock{}

	go func() {
		for !fileExists(filepath.Join(f.records, "args")) { // the agent is running
			time.Sleep(10 * time.Millisecond)
		}

		clock.fireNow()
	}()

	got := agent.Runner{AfterFunc: clock.AfterFunc}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeIdle, got.Outcome)
	assert.Equal(t, 7*time.Second, clock.limit)
	assert.Equal(t, 1, clock.stops)
}

func TestRunner_OutputResetsTheIdleTimer(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "chatter")
	f.spec.IdleTimeout = time.Minute
	clock := &fakeClock{}

	got := agent.Runner{AfterFunc: clock.AfterFunc}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Positive(t, clock.resetCount())
	assert.Contains(t, f.log.String(), "tick 6")
}

func TestRunner_WithoutAnIdleLimitNoTimerStarts(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "chatter")
	clock := &fakeClock{}

	got := agent.Runner{AfterFunc: clock.AfterFunc}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Nil(t, clock.fire)
	assert.Zero(t, clock.resetCount())
}

func TestRunner_TheRealIdleTimerStopsASilentAgent(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "silent")
	f.spec.IdleTimeout = 100 * time.Millisecond

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeIdle, got.Outcome)
}

func TestRunner_ACancelKillsTheAgentAndItsChildren(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "holdgrand")
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		for !fileExists(filepath.Join(f.records, "grandchild")) {
			time.Sleep(20 * time.Millisecond)
		}

		cancel()
	}()

	got := agent.Runner{}.Run(ctx, f.spec)

	assert.Equal(t, agent.OutcomeCancelled, got.Outcome)
	assertGone(t, f.records)
}

func TestRunner_ACancelBeforeTheStartReadsAsCancelled(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := agent.Runner{}.Run(ctx, f.spec)

	assert.Equal(t, agent.OutcomeCancelled, got.Outcome)
}

// runnerWaitDelay is the wait delay of the command that proc.GroupCommand builds. A kill by the wait delay cannot come sooner than this.
const runnerWaitDelay = time.Second

// The cancel kills the group at once, through the cancel function of the command. The wait delay of the command (one second) and the sweep after the wait
// would also end the group, but later: the child must be gone well before the wait delay.
func TestRunner_ACancelInsideOnStartKillsTheGroupBeforeTheWaitDelay(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "holdgrand")
	ctx, cancel := context.WithCancel(context.Background())

	started := 0
	goneAfter := make(chan time.Duration, 1)
	f.spec.OnStart = func(pid int) {
		started = pid

		for !fileExists(filepath.Join(f.records, "grandchild")) {
			time.Sleep(10 * time.Millisecond)
		}

		child, err := strconv.Atoi(record(t, f.records, "grandchild"))
		require.NoError(t, err)

		at := time.Now()

		cancel()

		go func() {
			for !errors.Is(syscall.Kill(child, 0), syscall.ESRCH) {
				time.Sleep(5 * time.Millisecond)
			}

			goneAfter <- time.Since(at)
		}()
	}

	got := agent.Runner{}.Run(ctx, f.spec)

	assert.Equal(t, agent.OutcomeCancelled, got.Outcome)
	require.NotZero(t, started, "the agent started before the cancel")
	assert.Less(t, <-goneAfter, runnerWaitDelay, "the child of the agent dies with the cancel, not with the wait delay")
	assertGone(t, f.records)
}

func TestRunner_ADeadlineOnTheCallerContextReadsAsTimedOut(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "silent")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	got := agent.Runner{}.Run(ctx, f.spec)

	assert.Equal(t, agent.OutcomeTimedOut, got.Outcome)
}

func TestRunner_ChildrenThatOutliveTheAgentAreKilled(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", claude.Name, "grand")

	f.spec.Log = nil

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.Result{Outcome: agent.OutcomeExited}, got)
	assertGone(t, f.records)
}

func assertGone(t *testing.T, records string) {
	t.Helper()

	pid, err := strconv.Atoi(record(t, records, "grandchild"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 10*time.Second, 50*time.Millisecond,
		"the child %d is still running", pid)
}

func TestRunner_DoesNotStart(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	t.Run("unknown_agent", func(t *testing.T) { //nolint:paralleltest // the parent test sets PATH
		f := newFixture(t, "claude", claude.Name, "")
		f.spec.Runner.Agent = model.Agent("pi")
		f.spec.Adapter = nil

		got := agent.Runner{}.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
		assert.Contains(t, got.Note, `agent "pi" cannot be started yet`)
	})

	t.Run("opencode_is_not_supported_yet", func(t *testing.T) { //nolint:paralleltest // the parent test sets PATH
		f := newFixture(t, "opencode", opencode.Name, "")

		got := agent.Runner{}.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
		assert.Contains(t, got.Note, `agent "opencode" cannot be started yet`)
		assert.NoFileExists(t, filepath.Join(f.records, "args"))
	})

	t.Run("missing_login_folder_is_not_created", func(t *testing.T) { //nolint:paralleltest // the parent test sets PATH
		f := newFixture(t, "claude", claude.Name, "")
		gone := filepath.Join(t.TempDir(), "no-login")
		f.spec.Runner.ConfigDir = gone

		got := agent.Runner{}.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
		assert.Contains(t, got.Note, "is not a folder")
		assert.NoDirExists(t, gone)
		assert.NoFileExists(t, filepath.Join(f.records, "args"))
	})

	t.Run("login_path_is_a_file", func(t *testing.T) { //nolint:paralleltest // the parent test sets PATH
		f := newFixture(t, "claude", claude.Name, "")
		f.spec.Runner.ConfigDir = filepath.Join(f.records, "mode")

		assert.Equal(t, agent.OutcomeNotStarted, agent.Runner{}.Run(context.Background(), f.spec).Outcome)
	})

	t.Run("binary_not_on_path", func(t *testing.T) { //nolint:paralleltest // the parent test sets PATH
		f := newFixture(t, "other", claude.Name, "")

		got := agent.Runner{}.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
		assert.Contains(t, got.Note, "claude is not installed")
	})

	t.Run("binary_only_on_the_parent_path", func(t *testing.T) { //nolint:paralleltest // the parent test sets PATH
		f := newFixture(t, "claude", claude.Name, "")
		f.spec.Env["PATH"] = "/bin:/usr/bin"

		got := agent.Runner{}.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
		assert.Contains(t, got.Note, "claude is not installed")
		assert.NoFileExists(t, filepath.Join(f.records, "args"))
	})

	t.Run("binary_cannot_start", func(t *testing.T) { //nolint:paralleltest // the parent test sets PATH
		f := newFixture(t, "claude", claude.Name, "")
		require.NoError(t, exectest.Write(filepath.Join(f.records, "claude"), "#!/nonexistent/interpreter\n"))

		got := agent.Runner{}.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
		assert.NotEmpty(t, got.Note)
	})
}

func fileExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}
