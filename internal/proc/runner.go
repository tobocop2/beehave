// Package proc runs outside commands.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
// MaxNewProcesses caps the tasks of the command and everything it starts at MaxNewProcesses besides the command itself. Zero means no cap. Threads count as tasks.
// On Linux the command runs in its own systemd user scope, so the kernel counts that tree alone, and a cancel kills the whole tree. If no scope can start, the call fails and the command does not run.
// Elsewhere the cap is the per-user limit: the count of the user at the start plus MaxNewProcesses, shared with the other programs of the user, and a cancel kills the process group only.
type ExecRunner struct {
	Binary          string
	Dir             string
	Timeout         time.Duration
	Env             []string
	Only            []string
	Stdin           string
	MaxNewProcesses int

	// abs makes a path absolute. Nil means filepath.Abs. Tests set it with WithAbs.
	abs func(string) (string, error)

	// hooks holds the system reads that tests replace.
	hooks platformHooks
}

// ExitCapFailed is the exit code of a call whose process cap could not be set. The command did not start.
// The result holds the cause in SetupErr. The exit code alone does not tell this failure from a command that exits with 125.
const ExitCapFailed = 125

// NewExecRunner returns a runner for the binary in dir that stops a call after DefaultTimeout.
func NewExecRunner(binary, dir string) ExecRunner {
	return ExecRunner{Binary: binary, Dir: dir, Timeout: DefaultTimeout}
}

// Run starts the binary in its own process group. A canceled call reads as exit code 130, a timeout as 124 and a binary that cannot start as 127.
func (r ExecRunner) Run(ctx context.Context, args []string) model.CommandResult {
	if code, note := interrupted(ctx); note != "" {
		return model.CommandResult{ExitCode: code, Stderr: r.Binary + ": " + note, TimedOut: code == exitTimeout}
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
	cmd, err := r.command(ctx, args)
	if err != nil {
		return model.CommandResult{ExitCode: ExitCapFailed, SetupErr: err}
	}

	if cmd.Err != nil {
		return model.CommandResult{ExitCode: exitNotFound, Stderr: r.Binary + ": command not found"}
	}

	if r.Stdin != "" {
		cmd.Stdin = strings.NewReader(r.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	return r.run(ctx, cmd, &stdout, &stderr)
}

// result turns the end of a run of cmd, with the text it wrote, into a result.
func (r ExecRunner) result(ctx context.Context, err error, stdout, stderr *bytes.Buffer) model.CommandResult {
	code, note := exitCodeOf(ctx, err)
	addNote(stderr, r.Binary, note)

	return model.CommandResult{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String(), TimedOut: err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)}
}

// addNote ends stderr with a line that names the binary and the note, when there is a note.
func addNote(stderr *bytes.Buffer, binary, note string) {
	if note == "" {
		return
	}

	if stderr.Len() > 0 && !bytes.HasSuffix(stderr.Bytes(), []byte("\n")) {
		stderr.WriteString("\n")
	}

	stderr.WriteString(binary + ": " + note)
}

// command returns the command to start with its whole environment.
// With a cap, the command runs under the cap of the platform (see cappedCommand). A binary that is not found keeps its name, so the call reads as not found and not as a cap failure.
func (r ExecRunner) command(ctx context.Context, args []string) (*exec.Cmd, error) {
	env := r.environment()

	if r.MaxNewProcesses > 0 {
		cmd, err := r.cappedCommand(ctx, args, env)
		if cmd != nil || err != nil {
			return cmd, err
		}
	}

	cmd := GroupCommand(ctx, r.Binary, args, r.Dir)
	cmd.Env = env

	return cmd, nil
}

// environment returns the environment of the command: Only, or the environment of this process, and then Env.
func (r ExecRunner) environment() []string {
	base := r.Only
	if base == nil {
		base = (&exec.Cmd{Dir: r.Dir}).Environ()
	}

	return append(slices.Clone(base), r.Env...)
}

// resolved returns the binary to look up. A program path with a separator is relative to Dir, because the program runs there, so the lookup joins it to Dir and makes it absolute.
// If the path cannot be made absolute, the call fails: a capped call must not run without its cap.
func (r ExecRunner) resolved() (string, error) {
	if r.Dir == "" || filepath.IsAbs(r.Binary) || filepath.Base(r.Binary) == r.Binary {
		return r.Binary, nil
	}

	abs := r.abs
	if abs == nil {
		abs = filepath.Abs
	}

	path, err := abs(filepath.Join(r.Dir, r.Binary))
	if err != nil {
		return "", fmt.Errorf("could not resolve %s in %s: %w", r.Binary, r.Dir, err)
	}

	return path, nil
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
