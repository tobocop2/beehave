// Package proc runs outside commands.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
// MaxNewProcesses sets the process limit of the user to the count of the user at the start plus MaxNewProcesses. Zero means no cap.
// The kernel counts every process of the user, not only those of the command, so the cap is shared with the other programs of the user.
// A value below about 10 is not exact on a busy machine, because other programs start and end processes while the command runs.
// A user with root rights has no cap. On Linux, the count and the kernel count each thread of the user.
type ExecRunner struct {
	Binary          string
	Dir             string
	Timeout         time.Duration
	Env             []string
	Only            []string
	Stdin           string
	MaxNewProcesses int

	// count returns the count of the processes of the user. Nil means the count of the system: ps on macOS, /proc on Linux. Tests set it with WithCount.
	count func() (int, error)

	// abs makes a path absolute. Nil means filepath.Abs. Tests set it with WithAbs.
	abs func(string) (string, error)

	// executable returns the path of the running program, which the cap helper re-executes. Nil means os.Executable. Tests set it with WithExecutable.
	executable func() (string, error)
	// procSelf is the path of the running program image on Linux. Empty means /proc/self/exe. Tests set it with WithProcSelf.
	procSelf string
}

// ExitCapFailed is the exit code of a call whose process cap could not be counted or set. The command did not start.
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
	cmd, capped, err := r.command(ctx, args)
	if err != nil {
		return model.CommandResult{ExitCode: ExitCapFailed, SetupErr: err}
	}

	if cmd.Err != nil {
		return model.CommandResult{ExitCode: exitNotFound, Stderr: r.Binary + ": command not found"}
	}

	r.feed(cmd)

	return r.collect(ctx, cmd, capped)
}

// collect runs cmd to its end and returns its result. With a cap, cmd gets a pipe as file descriptor 3.
// Text on that pipe means the helper did not set the limit: the result then holds it in SetupErr, and nothing else.
func (r ExecRunner) collect(ctx context.Context, cmd *exec.Cmd, capped bool) model.CommandResult {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	read, err := runWithPipe(cmd, capped)

	if cause := pipeError(read); cause != nil {
		return model.CommandResult{ExitCode: ExitCapFailed, SetupErr: fmt.Errorf("could not set max_new_processes = %d: %w", r.MaxNewProcesses, cause)}
	}

	code, note := exitCodeOf(ctx, err)
	addNote(&stderr, r.Binary, note)

	return model.CommandResult{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String(), TimedOut: err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)}
}

// runWithPipe runs cmd to its end. With a cap, cmd gets the write end of a pipe as file descriptor 3, and the read end is returned. Without a cap, the read end is nil.
// The parent closes its copy of the write end after the run, so a read of the returned end stops at the end of the pipe.
func runWithPipe(cmd *exec.Cmd, capped bool) (*os.File, error) {
	var read, write *os.File

	var err error

	if capped {
		read, write, err = os.Pipe()
		cmd.ExtraFiles = []*os.File{write}
	}

	if err == nil {
		err = cmd.Run()
	}

	_ = write.Close()

	return read, err
}

// pipeError reads read to its end and closes it. It returns the text as an error, or nil for an empty pipe or no pipe.
// The call comes after the command ends. At that time no process holds the write end: the helper closes it before exec, and the parent has closed its copy. So the read ends at the end of the pipe.
func pipeError(read *os.File) error {
	if read == nil {
		return nil
	}

	data, err := io.ReadAll(read)
	_ = read.Close()

	return errors.Join(err, textError(strings.TrimSpace(string(data))))
}

// textError returns an error that holds text, or nil for an empty text.
func textError(text string) error {
	if text == "" {
		return nil
	}

	return errors.New(text)
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

// command returns the command to start, and whether it is the helper of the cap. With a cap, it counts the processes of the user, then returns this program started as the helper (see capexec.go): it sets the limit with setrlimit and runs the binary.
// A binary that is not found keeps its name, so the call reads as not found and not as a helper failure.
func (r ExecRunner) command(ctx context.Context, args []string) (*exec.Cmd, bool, error) {
	if r.MaxNewProcesses > 0 {
		cmd, err := r.cappedCommand(ctx, args)
		if cmd != nil || err != nil {
			return cmd, cmd != nil, err
		}
	}

	return GroupCommand(ctx, r.Binary, args, r.Dir), false, nil
}

// cappedCommand returns the helper of the cap for a binary that is found. It returns nil and no error for a binary that is not found.
func (r ExecRunner) cappedCommand(ctx context.Context, args []string) (*exec.Cmd, error) {
	target, err := r.resolved()
	if err != nil {
		return nil, err
	}

	found, err := exec.LookPath(target)
	if err != nil {
		return nil, nil //nolint:nilerr // a binary that is not found is reported by the plain command
	}

	limit, err := r.limit()
	if err != nil {
		return nil, err
	}

	return r.helperCommand(ctx, limit, found, args)
}

// helperCommand returns this program, started with the name of the cap helper, for the limit and the program found.
func (r ExecRunner) helperCommand(ctx context.Context, limit int, found string, args []string) (*exec.Cmd, error) {
	executable := r.executable
	if executable == nil {
		executable = os.Executable
	}

	self, err := executable()
	if err != nil {
		return nil, fmt.Errorf("could not find the running program: %w", err)
	}

	self, err = r.startablePath(self)
	if err != nil {
		return nil, err
	}

	cmd := GroupCommand(ctx, self, nil, r.Dir)
	cmd.Args = append([]string{capArg0, strconv.Itoa(limit), found}, args...)

	return cmd, nil
}

// startablePath returns a path that starts this program. If the file at self is gone (the program was replaced or deleted after it started), Linux still runs the open image through /proc/self/exe.
// Elsewhere the call fails with the path of this program: the step then reads as a cap failure, not as a missing wrapped command.
func (r ExecRunner) startablePath(self string) (string, error) {
	if info, err := os.Stat(self); err == nil && !info.IsDir() && info.Mode().Perm()&executableBits != 0 {
		return self, nil
	}

	procSelf := r.procSelf
	if procSelf == "" {
		procSelf = "/proc/self/exe"
	}

	if _, err := os.Stat(procSelf); err == nil {
		return procSelf, nil
	}

	return "", fmt.Errorf("could not set max_new_processes = %d: the running program %s cannot start", r.MaxNewProcesses, self)
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

// limit returns the process limit to set:the count of the user now plus MaxNewProcesses.
func (r ExecRunner) limit() (int, error) {
	count := r.count
	if count == nil {
		count = userProcesses
	}

	now, err := count()
	if err != nil {
		return 0, fmt.Errorf("could not count the user's processes: %w", err)
	}

	return now + r.MaxNewProcesses, nil
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
