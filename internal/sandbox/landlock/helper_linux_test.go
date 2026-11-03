package landlock

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	ll "github.com/landlock-lsm/go-landlock/landlock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/sandbox"
)

const (
	scriptMode = 0o700
	runTimeout = time.Minute
)

func withABI(abi int, err error) Enforcer {
	e := New()
	e.abi = func() (int, error) { return abi, err }

	return e
}

// asHelper makes e start program in the place of the helper, as a stand-in for a child that the kernel or the container lets confine, or not.
func asHelper(e Enforcer, program string) Enforcer {
	e.executable = func() (string, error) { return program, nil }

	return e
}

func TestCheck_AChildThatCannotApplyTheConfinementMakesTheEnforcerUnavailableAtAnyABI(t *testing.T) {
	t.Parallel()

	for _, abi := range []int{8, 9, 11} {
		// The shell rejects the helper argument and exits at once, as the helper exits 125 when the kernel rejects the filter or the Landlock configuration.
		err := asHelper(withABI(abi, nil), "/bin/sh").Check()
		require.ErrorIs(t, err, sandbox.ErrUnavailable, "ABI %d", abi)
		require.ErrorContains(t, err, "cannot apply the confinement here")

		require.NoError(t, asHelper(withABI(abi, nil), "/bin/true").Check(), "a child that confines and runs gives no reason to refuse, ABI %d", abi)
	}
}

func TestCheck_TheProbeRunsTheRealHelperUnderTheFilterAndTheRulesOfThisKernel(t *testing.T) {
	t.Parallel()

	abi, err := ABI()
	if err != nil || abi < minABI {
		t.Skip("this kernel cannot run the enforcer")
	}

	require.NoError(t, New().Check())
}

func TestCheck_AnExecutableOrAPlanThatCannotBeMadeIsReportedAsUnavailable(t *testing.T) {
	t.Parallel()

	e := withABI(8, nil)
	e.executable = func() (string, error) { return "", errors.New("no executable") }
	require.ErrorIs(t, e.Check(), sandbox.ErrUnavailable)

	e = withABI(8, nil)
	e.marshal = func(any) ([]byte, error) { return nil, errors.New("no plan") }
	require.ErrorContains(t, e.Check(), "no plan")
}

func TestCheck_ABIBelow4IsRefusedBecauseTheKernelCannotLimitTCPConnect(t *testing.T) {
	t.Parallel()

	assert.Equal(t, Name, New().Name())

	err := withABI(3, nil).Check()
	require.ErrorIs(t, err, sandbox.ErrUnavailable)
	require.ErrorContains(t, err, "ABI 3")
	require.ErrorContains(t, err, "limit TCP connect")

	require.NoError(t, asHelper(withABI(4, nil), "/bin/true").Check())
	require.NoError(t, asHelper(withABI(8, nil), "/bin/true").Check())
}

func TestCheck_AKernelWithoutLandlockIsRefusedWithTheKernelVersionToUse(t *testing.T) {
	t.Parallel()

	failure := errors.New("function not implemented")

	err := withABI(0, failure).Check()
	require.ErrorIs(t, err, sandbox.ErrUnavailable)
	require.ErrorIs(t, err, failure)
	assert.ErrorContains(t, err, "Linux 6.7 or later")
}

func exitOf(t *testing.T, line string) *exec.ExitError {
	t.Helper()

	err := exec.CommandContext(t.Context(), "sh", "-c", line).Run() //nolint:gosec // a test runs sh on a fixed line
	require.Error(t, err)

	var exited *exec.ExitError
	require.ErrorAs(t, err, &exited)

	return exited
}

func TestCodeOf_AnExitCodePassesThroughAndASignalGives128PlusItsNumber(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 7, codeOf(exitOf(t, "exit 7")))
	assert.Equal(t, 128+9, codeOf(exitOf(t, "kill -9 $$")))
}

func script(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), scriptMode))
	require.NoError(t, os.Chmod(path, scriptMode))

	return path
}

func TestFindProgram_FollowsThePathOfTheCommandAndNeverThePathOfObee(t *testing.T) {
	t.Parallel()

	first, second := t.TempDir(), t.TempDir()
	onlyFirst := script(t, first, "only-first")
	onlySecond := script(t, second, "only-second")

	found, err := findProgram("only-second", []string{"HOME=/h", "PATH=" + first + ":" + second})
	require.NoError(t, err)
	assert.Equal(t, onlySecond, found, "a later folder of PATH is searched")

	found, err = findProgram("only-first", []string{"PATH=" + first})
	require.NoError(t, err)
	assert.Equal(t, onlyFirst, found)

	found, err = findProgram(onlyFirst, nil)
	require.NoError(t, err)
	assert.Equal(t, onlyFirst, found, "a name with a slash is taken as it is")

	_, err = findProgram(filepath.Join(first, "missing"), nil)
	require.Error(t, err)

	for _, env := range [][]string{{"PATH=" + first}, {"PATH="}, {"HOME=/h"}, nil} {
		_, err = findProgram("sh", env)
		var missing *exec.Error
		require.ErrorAs(t, err, &missing, "env %v finds no sh", env)
		assert.Equal(t, "sh", missing.Name)
		assert.ErrorIs(t, err, exec.ErrNotFound)
	}
}

func TestRestrict_OneRulePerPathAndOneForTheProxyPortAndAKernelFailureIsReported(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := script(t, dir, "tool")
	plan := sandbox.Plan{Read: []string{dir, file, filepath.Join(dir, "missing")}, Write: []string{dir}}

	var got []ll.Rule

	capture := func(rules ...ll.Rule) error {
		got = rules

		return nil
	}

	require.NoError(t, restrict(spec{Plan: plan, Port: 8080}, capture))
	assert.Len(t, got, 5, "three reads, one write, and the proxy port")

	require.NoError(t, restrict(spec{Plan: plan}, capture))
	assert.Len(t, got, 4, "no proxy port, no connect rule")

	failure := errors.New("the kernel said no")
	err := restrict(spec{Plan: plan}, func(...ll.Rule) error { return failure })
	require.ErrorIs(t, err, failure)
	assert.ErrorContains(t, err, "apply the Landlock rules")
}

func TestRules_AFolderAndAFileGetTheirOwnKindOfRule(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := script(t, dir, "tool")

	assert.NotEqual(t, readRule(dir).String(), readRule(file).String())
	assert.NotEqual(t, writeRule(dir).String(), writeRule(file).String())
	assert.NotEqual(t, readRule(dir).String(), writeRule(dir).String())
	assert.True(t, isFolder(filepath.Join(dir, "missing")), "a path that does not exist yet is taken as a folder, and the rule ignores it while it is missing")
}

// fakeSystem records what the helper asked of the kernel.
type fakeSystem struct {
	abiIs      int
	abiBy      error
	filtered   bool
	filterBy   error
	config     ll.Config
	restricted bool
	executed   []string
	restrictBy error
	executeBy  error
}

func (f *fakeSystem) system() system {
	return system{
		abi: func() (int, error) { return f.abiIs, f.abiBy },
		filter: func() error {
			f.filtered = true

			return f.filterBy
		},
		restrict: func(cfg ll.Config, _ ...ll.Rule) error {
			f.restricted = true
			f.config = cfg

			return f.restrictBy
		},
		execve: func(program string, argv, _ []string) error {
			f.executed = append([]string{program}, argv...)

			return f.executeBy
		},
	}
}

func TestBecomeCommand_ABadPlanOrAMissingProgramOrAFailedRestrictRunsNoCode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tool := script(t, dir, "tool")
	plan := `{"argv":["` + tool + `","a"],"env":[]}`

	cases := []struct {
		name     string
		raw      string
		fake     fakeSystem
		wantCode int
		wantText string
	}{
		{"text that is not a plan", "not json", fakeSystem{}, helperFailed, "read the sandbox plan"},
		{"a plan without a command", `{"argv":[]}`, fakeSystem{}, helperFailed, "has no command"},
		{"a program that is not found", `{"argv":["no-such-program"],"env":["PATH=` + dir + `"]}`, fakeSystem{}, notFound, "no-such-program"},
		{"a kernel that refuses the rules", plan, fakeSystem{restrictBy: errors.New("refused")}, helperFailed, "refused"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			code, err := becomeCommand(c.raw, c.fake.system())
			assert.Equal(t, c.wantCode, code)
			require.ErrorContains(t, err, c.wantText)
			assert.Empty(t, c.fake.executed, "the command did not start")
		})
	}

	refused := fakeSystem{restrictBy: errors.New("refused")}
	_, err := becomeCommand(plan, refused.system())
	require.Error(t, err)
	assert.True(t, refused.restricted)

	missing := fakeSystem{}
	_, err = becomeCommand(`{"argv":["no-such-program"],"env":["PATH=`+dir+`"]}`, missing.system())
	require.Error(t, err)
	assert.False(t, missing.restricted, "a program that is not found is reported before any rule is applied")
}

func TestConfigFor_FromABI9TheKernelClosesUnixSockets(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ll.V9, configFor(9))
	assert.Equal(t, ll.V9, configFor(11))
	assert.Equal(t, ll.V4, configFor(8))
	assert.Equal(t, ll.V4, configFor(4))
}

func TestBecomeCommand_BelowABI9TheSocketFilterLoadsFirstAndAFailureStopsTheCommand(t *testing.T) {
	t.Parallel()

	tool := script(t, t.TempDir(), "tool")
	plan := `{"argv":["` + tool + `"],"env":[]}`

	old := fakeSystem{abiIs: 8}
	_, err := becomeCommand(plan, old.system())
	require.NoError(t, err)
	assert.True(t, old.filtered, "ABI 8 does not govern unix sockets, so the filter loads")
	assert.Equal(t, ll.V4, old.config)

	newer := fakeSystem{abiIs: 9}
	_, err = becomeCommand(plan, newer.system())
	require.NoError(t, err)
	assert.False(t, newer.filtered, "ABI 9 closes unix sockets by itself")
	assert.Equal(t, ll.V9, newer.config)

	broken := fakeSystem{abiIs: 8, filterBy: errors.New("no seccomp")}
	code, err := becomeCommand(plan, broken.system())
	require.ErrorContains(t, err, "no seccomp")
	assert.Equal(t, helperFailed, code)
	assert.False(t, broken.restricted, "a command never runs without the filter")
	assert.Empty(t, broken.executed)

	blind := fakeSystem{abiBy: errors.New("no landlock")}
	code, err = becomeCommand(plan, blind.system())
	require.ErrorContains(t, err, "read the Landlock ABI")
	assert.Equal(t, helperFailed, code)
	assert.Empty(t, blind.executed)
}

func TestBecomeCommand_TheRulesApplyBeforeTheCommandReplacesTheHelper(t *testing.T) {
	t.Parallel()

	tool := script(t, t.TempDir(), "tool")
	failure := errors.New("exec failed")
	fake := fakeSystem{executeBy: failure}

	code, err := becomeCommand(`{"argv":["`+tool+`","a","b"],"env":["X=1"]}`, fake.system())
	require.ErrorIs(t, err, failure, "the error of exec is the error of the helper, since exec returns only on failure")
	assert.Equal(t, helperFailed, code)
	assert.True(t, fake.restricted)
	assert.Equal(t, []string{tool, tool, "a", "b"}, fake.executed)
}

func TestRunHelper_APlanThatDoesNotReadWritesTheReasonToStderr(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer

	code, helper := RunHelper([]string{"obee", helperArg, "not json"}, &stderr)
	assert.True(t, helper)
	assert.Equal(t, helperFailed, code)
	assert.Contains(t, stderr.String(), "obee sandbox: read the sandbox plan")
	assert.Contains(t, stderr.String(), "The command did not start.")

	_, helper = RunHelper([]string{"obee", helperArg}, &stderr)
	assert.False(t, helper, "the plan is the third argument")
}

func runnable(t *testing.T) Enforcer {
	t.Helper()

	e := New()
	if err := e.Check(); err != nil {
		t.Skipf("Landlock cannot run on this machine: %v", err)
	}

	return e
}

func run(t *testing.T, e Enforcer, ctx context.Context, line string, cmd sandbox.Command) (int, error) { //nolint:revive // the context follows the enforcer to read like Run
	t.Helper()

	cmd.Argv, cmd.Dir, cmd.Env = []string{"sh", "-c", line}, t.TempDir(), []string{"PATH=/usr/bin:/bin"}

	return e.Run(ctx, sandbox.Plan{Read: sandbox.SystemRead(), Write: sandbox.SystemWrite()}, 0, cmd)
}

func TestRun_TheExitCodeOfTheCommandPassesThroughAndItsOutputGoesToTheWriters(t *testing.T) {
	t.Parallel()

	e := runnable(t)

	var out, errOut bytes.Buffer

	code, err := run(t, e, t.Context(), "echo to-out; echo to-err >&2; exit 7", sandbox.Command{Stdout: &out, Stderr: &errOut})
	require.NoError(t, err)
	assert.Equal(t, 7, code)
	assert.Equal(t, "to-out\n", out.String())
	assert.Equal(t, "to-err\n", errOut.String())

	code, err = run(t, e, t.Context(), "echo dropped; exit 0", sandbox.Command{})
	require.NoError(t, err)
	assert.Zero(t, code, "a command with no writers still runs")

	code, err = run(t, e, t.Context(), "no-such-program-here", sandbox.Command{})
	require.NoError(t, err)
	assert.Equal(t, notFound, code)

	code, err = run(t, e, t.Context(), "kill -9 $$", sandbox.Command{})
	require.NoError(t, err)
	assert.Equal(t, 128+9, code, "a signal gives 128 plus its number")
}

func TestRun_ACanceledContextKillsTheWholeGroupOfTheCommand(t *testing.T) {
	t.Parallel()

	e := runnable(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var out syncBuffer

	type result struct {
		code int
		err  error
	}

	done := make(chan result, 1)

	go func() {
		code, err := run(t, e, ctx, "sleep 30 & echo $!; wait", sandbox.Command{Stdout: &out})
		done <- result{code, err}
	}()

	// The cancel waits until the command prints the pid of its child, so the group exists before it dies, however long the helper took to start.
	var child int

	require.Eventually(t, func() bool {
		pid, err := strconv.Atoi(strings.TrimSpace(out.String()))
		child = pid

		return err == nil
	}, 20*time.Second, 5*time.Millisecond, "the command did not print the pid of its child")

	cancel()

	select {
	case got := <-done:
		require.NoError(t, got.err)
		assert.Equal(t, 128+9, got.code)
	case <-time.After(runTimeout / 2):
		t.Fatal("the command outlived its canceled context")
	}

	// The background sleep dies with the group. The wait is for the signal to land, and it ends at once when the child is gone.
	assert.Eventually(t, func() bool { return errors.Is(syscall.Kill(child, 0), syscall.ESRCH) }, 20*time.Second, 10*time.Millisecond, "the background sleep is still alive")
}

// syncBuffer is a writer that the test reads while the command still writes to it.
type syncBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.data.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.data.String()
}

func TestRun_AHelperThatCannotStartIsAnErrorAndRunsNoCommand(t *testing.T) {
	t.Parallel()

	missing := errors.New("no path")
	e := New()
	e.executable = func() (string, error) { return "", missing }

	_, err := run(t, e, t.Context(), "exit 0", sandbox.Command{})
	require.ErrorIs(t, err, missing)
	require.ErrorContains(t, err, "find the obee program")

	e = New()
	e.executable = func() (string, error) { return filepath.Join(t.TempDir(), "gone"), nil }

	_, err = run(t, e, t.Context(), "exit 0", sandbox.Command{})
	require.ErrorContains(t, err, "start the sandbox helper")

	unwritable := errors.New("no encoding")
	e = New()
	e.marshal = func(any) ([]byte, error) { return nil, unwritable }

	_, err = run(t, e, t.Context(), "exit 0", sandbox.Command{})
	require.ErrorIs(t, err, unwritable)
	assert.ErrorContains(t, err, "write the sandbox plan")
}
