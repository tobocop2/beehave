// Package proc runs outside commands.
package proc

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/tobocop2/obee/internal/model"
)

const executableBits os.FileMode = 0o111

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

// ExecRunner runs a binary found on PATH. A zero Timeout means no limit. Env holds NAME=value pairs that the command gets on top of its base environment.
// The base environment is that of the process, unless Only is not nil: then it is Only, and no variable of the process reaches the command.
// Stdin is the text that the command reads. An empty Stdin is an empty input.
type ExecRunner struct {
	Binary  string
	Dir     string
	Timeout time.Duration
	Env     []string
	Only    []string
	Stdin   string
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

	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}

	return r.execute(ctx, args)
}

// GroupCommand returns a command for a binary found on PATH that runs in its own process group. A canceled context kills the whole group. A binary that is not found is in cmd.Err.
func GroupCommand(ctx context.Context, binary string, args []string, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // fixed binary name and argument list, no shell
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return KillGroup(cmd) }

	return cmd
}

// LookPath returns the executable that binary names in the colon-separated folders of path.
// An empty path means the process PATH. A folder that is not an absolute path is skipped: the command would run from its own working folder.
func LookPath(binary, path string) (string, error) {
	if path == "" {
		return exec.LookPath(binary)
	}

	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}

		found := filepath.Join(dir, binary)
		if info, err := os.Stat(found); err == nil && !info.IsDir() && info.Mode().Perm()&executableBits != 0 {
			return found, nil
		}
	}

	return "", exec.ErrNotFound
}

// KillGroup sends SIGKILL to the process group of a started command. It fails with ESRCH when no member is left.
func KillGroup(cmd *exec.Cmd) error {
	return KillGroupID(cmd.Process.Pid)
}

func (r ExecRunner) execute(ctx context.Context, args []string) model.CommandResult {
	cmd := GroupCommand(ctx, r.Binary, args, r.Dir)
	if cmd.Err != nil {
		return model.CommandResult{ExitCode: exitNotFound, Stderr: r.Binary + ": command not found"}
	}

	r.feed(cmd)

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

// feed sets the environment and the stdin of cmd. An empty Stdin leaves the input empty (the null device).
func (r ExecRunner) feed(cmd *exec.Cmd) {
	base := r.Only
	if base == nil {
		base = cmd.Environ()
	}

	cmd.Env = append(slices.Clone(base), r.Env...)

	if r.Stdin != "" {
		cmd.Stdin = strings.NewReader(r.Stdin)
	}
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
