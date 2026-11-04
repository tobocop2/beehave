package contract

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/tobocop2/beehave/internal/sandbox"
)

// recorder is a reporter that keeps what the checks report, so a test can read it. It never stops the check that reports.
type recorder struct {
	t      *testing.T
	errors []string
	fatals []string
}

func (*recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recorder) Fatal(args ...any) { r.fatals = append(r.fatals, fmt.Sprint(args...)) }

func (r *recorder) TempDir() string { return r.t.TempDir() }

func (r *recorder) Cleanup(f func()) { r.t.Cleanup(f) }

// unconfined is an enforcer that confines nothing: it runs the command as it is.
type unconfined struct{ checkErr error }

func (unconfined) Name() string { return "unconfined" }

func (u unconfined) Check() error { return u.checkErr }

func (unconfined) Run(ctx context.Context, _ sandbox.Plan, _ int, cmd sandbox.Command) (int, error) {
	child := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...) //nolint:gosec // a test runs the command of its own plan
	child.Dir, child.Env, child.Stdout, child.Stderr = cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr

	err := child.Run()

	var exited *exec.ExitError
	if errors.As(err, &exited) {
		return exited.ExitCode(), nil
	}

	return 0, err
}

func assertNone(t *testing.T, r *recorder) {
	t.Helper()

	if len(r.errors) != 0 || len(r.fatals) != 0 {
		t.Errorf("the reporter got failures: errors %q, fatals %q", r.errors, r.fatals)
	}
}

func TestExpectAndMust_ReportOnlyAFailure(t *testing.T) {
	t.Parallel()

	r := &recorder{t: t}

	expect(r, true, "fine")
	must(r, nil)
	assertNone(t, r)

	expect(r, false, "bad %d", 3)
	must(r, errors.New("broken"))

	if len(r.errors) != 1 || r.errors[0] != "bad 3" {
		t.Errorf("expect reported %q, want one message %q", r.errors, "bad 3")
	}

	if len(r.fatals) != 1 || r.fatals[0] != "broken" {
		t.Errorf("must reported %q, want %q", r.fatals, "broken")
	}
}

func TestRequireRunnable_StopsTheSuiteForAnEnforcerThatCannotRun(t *testing.T) {
	t.Parallel()

	r := &recorder{t: t}

	requireRunnable(r, unconfined{})
	assertNone(t, r)

	requireRunnable(r, unconfined{checkErr: sandbox.ErrUnavailable})

	if len(r.fatals) != 1 || !strings.Contains(r.fatals[0], "needs an enforcer that can run here") {
		t.Errorf("the suite did not stop for an enforcer that cannot run: %q", r.fatals)
	}
}

func TestChecks_PassForTheOnesAnUnconfinedEnforcerCannotBreakAndFailForTheRest(t *testing.T) {
	t.Parallel()

	e := unconfined{}

	cases := []struct {
		name      string
		check     func(reporter, sandbox.Enforcer)
		wantError bool
	}{
		{"the exit code passes through without a sandbox", exitCode, false},
		{"the proxy refuses an unlisted domain without a sandbox", domains, false},
		{"a read under home is not refused without a sandbox", readUnderHome, true},
		{"a write outside the plan is not refused without a sandbox", writeOutside, true},
		{"a connection that skips the proxy is not refused without a sandbox", direct, true},
		{"a plan with no network still connects without a sandbox", noNetwork, true},
		{"a unix socket outside the plan is reached without a sandbox", unixSocket, true},
		{"a new top-level file is made without a sandbox", wholeFolder, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			r := &recorder{t: t}
			c.check(r, e)

			if len(r.fatals) != 0 {
				t.Fatalf("the check stopped: %q", r.fatals)
			}

			if got := len(r.errors) > 0; got != c.wantError {
				t.Errorf("the check reported failures = %t (%q), want %t", got, r.errors, c.wantError)
			}
		})
	}
}

// readOnly is an enforcer that refuses every command, as one that grants a folder entry by entry would refuse a new top-level file.
type readOnly struct{ unconfined }

func (readOnly) Run(context.Context, sandbox.Plan, int, sandbox.Command) (int, error) { return 1, nil }

func TestWholeFolder_FailsForAnEnforcerThatCannotMakeANewTopLevelEntry(t *testing.T) {
	t.Parallel()

	r := &recorder{t: t}
	wholeFolder(r, readOnly{})

	if len(r.errors) != 2 {
		t.Errorf("the check reported %q, want one failure for the make and one for the remove", r.errors)
	}
}

func TestAnswer_EndsWithoutAnAnswerWhenTheConnectionClosesBeforeTheHeadIsComplete(t *testing.T) {
	t.Parallel()

	server, client := net.Pipe()
	done := make(chan struct{})

	go func() {
		answer(server)
		close(done)
	}()

	if _, err := client.Write([]byte("GET / HTTP/1.1\r\n")); err != nil {
		t.Fatal(err)
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("answer kept the connection after the client closed it")
	}
}

func TestFetch_BuildsTheCurlLineWithTheExtraFlags(t *testing.T) {
	t.Parallel()

	line := fetch("http://127.0.0.1:1", "--insecure")
	if !strings.Contains(line, "--insecure") || !strings.HasSuffix(line, "http://127.0.0.1:1") {
		t.Errorf("fetch = %q", line)
	}
}
