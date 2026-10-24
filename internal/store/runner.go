// Package store reads and writes work items through the bd and gh command lines.
package store

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"

	"github.com/tobocop2/obee/internal/model"
)

// Exit codes a shell reports for a command that cannot start, one that ran too long and one that was canceled.
const (
	exitNotFound  = 127
	exitTimeout   = 124
	exitCancelled = 130
)

// DefaultTimeout is the longest one call of a bd or gh command may run.
const DefaultTimeout = 120 * time.Second

// Runner runs one outside command with the arguments and returns what it printed.
type Runner interface {
	Run(ctx context.Context, args []string) model.CommandResult
}

// ExecRunner runs a binary found on PATH. A zero Timeout means no limit.
type ExecRunner struct {
	Binary  string
	Dir     string
	Timeout time.Duration
}

// NewExecRunner returns a runner for the binary in dir that stops a call after DefaultTimeout.
func NewExecRunner(binary, dir string) ExecRunner {
	return ExecRunner{Binary: binary, Dir: dir, Timeout: DefaultTimeout}
}

// Run starts the binary in its own process group. A canceled call reads as exit code 130, a timeout as 124 and a binary that cannot start as 127.
func (r ExecRunner) Run(ctx context.Context, args []string) model.CommandResult {
	if code, note := interrupted(ctx); note != "" {
		return model.CommandResult{ExitCode: code, Stderr: r.Binary + ": " + note}
	}

	path, err := exec.LookPath(r.Binary)
	if err != nil {
		return model.CommandResult{ExitCode: exitNotFound, Stderr: r.Binary + ": command not found"}
	}

	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}

	return r.execute(ctx, path, args)
}

func (r ExecRunner) execute(ctx context.Context, path string, args []string) model.CommandResult {
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // fixed binary name and argument list, no shell
	cmd.Dir = r.Dir
	cmd.WaitDelay = time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	code, note := exitCodeOf(ctx, cmd.Run())
	if note != "" {
		if stderr.Len() > 0 && !bytes.HasSuffix(stderr.Bytes(), []byte("\n")) {
			stderr.WriteString("\n")
		}

		stderr.WriteString(r.Binary + ": " + note)
	}

	return model.CommandResult{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

// interrupted reports a context that is over: exit code 124 with the note "timed out" or 130 with the note for a canceled call.
func interrupted(ctx context.Context) (int, string) {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return exitTimeout, "timed out"
	case ctx.Err() != nil:
		return exitCancelled, "cancelled" //nolint:misspell // the note text is part of the contract with the coordinator
	}

	return 0, ""
}

func exitCodeOf(ctx context.Context, err error) (int, string) {
	if err == nil {
		return 0, ""
	}

	if code, note := interrupted(ctx); note != "" {
		return code, note
	}

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), ""
	}

	return exitNotFound, err.Error()
}
