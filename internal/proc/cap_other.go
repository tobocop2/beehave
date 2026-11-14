//go:build !linux

package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// platformHooks holds the system reads that the per-user cap needs. A nil field means the system read; tests supply others.
type platformHooks struct {
	// count returns the count of the processes of the user. Nil means ps.
	count func() (int, error)

	// executable returns the path of the running program, which the cap helper re-executes. Nil means os.Executable.
	executable func() (string, error)
}

// cappedCommand returns the helper of the cap for a binary that is found.
// It returns no command and no error for a binary that is not found, so that the plain command reports it.
func (r ExecRunner) cappedCommand(ctx context.Context, args, env []string) (*exec.Cmd, error) {
	target, err := r.resolved()
	if err != nil {
		return nil, err
	}

	found, err := exec.LookPath(target)
	if err != nil {
		return nil, nil //nolint:nilerr // a binary that is not found is reported by the plain command
	}

	cmd, err := r.capHelper(ctx, found, args)
	if err != nil {
		return nil, err
	}

	cmd.Env = env

	return cmd, nil
}

// capHelper returns the helper command that sets the process limit, then runs found with args.
func (r ExecRunner) capHelper(ctx context.Context, found string, args []string) (*exec.Cmd, error) {
	limit, err := r.limit()
	if err != nil {
		return nil, err
	}

	return r.helperCommand(ctx, limit, found, args)
}

// run runs cmd to its end and returns its result. The helper of a cap gets the write end of a pipe as file descriptor 3.
// Text on that pipe means the helper did not set the limit: the result then holds it in SetupErr, and nothing else.
func (r ExecRunner) run(ctx context.Context, cmd *exec.Cmd, stdout, stderr *bytes.Buffer) model.CommandResult {
	if cmd.Args[0] != capArg0 {
		return r.result(ctx, cmd.Run(), stdout, stderr)
	}

	read, write, err := os.Pipe()
	if err != nil {
		return model.CommandResult{ExitCode: ExitCapFailed, SetupErr: fmt.Errorf("could not make the setup pipe of the cap: %w", err)}
	}

	cmd.ExtraFiles = []*os.File{write}
	runErr := cmd.Run()
	_ = write.Close()

	if cause := r.setupFailure(read); cause != nil {
		return model.CommandResult{ExitCode: ExitCapFailed, SetupErr: cause}
	}

	return r.result(ctx, runErr, stdout, stderr)
}

// setupFailure reads read to its end and closes it. It returns the text as an error, or nil for an empty pipe.
// The call comes after the command ends. At that time no process holds the write end: the helper closes it before exec, and the parent has closed its copy. So the read ends at the end of the pipe.
func (r ExecRunner) setupFailure(read *os.File) error {
	data, err := io.ReadAll(read)
	_ = read.Close()

	cause := errors.Join(err, textError(strings.TrimSpace(string(data))))
	if cause == nil {
		return nil
	}

	return fmt.Errorf("could not set max_new_processes = %d: %w", r.MaxNewProcesses, cause)
}

// textError returns an error that holds text, or nil for an empty text.
func textError(text string) error {
	if text == "" {
		return nil
	}

	return errors.New(text)
}

// helperCommand returns this program, started with the name of the cap helper, for the limit and the program found.
func (r ExecRunner) helperCommand(ctx context.Context, limit int, found string, args []string) (*exec.Cmd, error) {
	executable := r.hooks.executable
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

// startablePath returns self when it starts this program. If the file at self is gone (the program was replaced or deleted after it started), the call fails with the path of this program:
// the step then reads as a cap failure, not as a missing wrapped command.
func (r ExecRunner) startablePath(self string) (string, error) {
	if info, err := os.Stat(self); err == nil && !info.IsDir() && info.Mode().Perm()&executableBits != 0 {
		return self, nil
	}

	return "", fmt.Errorf("could not set max_new_processes = %d: the running program %s cannot start", r.MaxNewProcesses, self)
}

// limit returns the process limit to set: the count of the user now plus MaxNewProcesses.
func (r ExecRunner) limit() (int, error) {
	count := r.hooks.count
	if count == nil {
		count = userProcesses
	}

	now, err := count()
	if err != nil {
		return 0, fmt.Errorf("could not count the user's processes: %w", err)
	}

	return now + r.MaxNewProcesses, nil
}
