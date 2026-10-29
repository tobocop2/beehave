package proc

import (
	"context"
	"errors"
	"os/exec"
	"syscall"

	"github.com/tobocop2/obee/internal/model"
)

const (
	shellBinary = "/bin/sh"
	// exitSignalBase is added to the signal number when a signal ends a command, as a shell reports it.
	exitSignalBase = 128
)

// RunShell runs the command line with /bin/sh -c in dir, in its own process group, and waits for it. The output holds both streams in the order written: the last model.ReportLimit bytes of them.
// The code is the exit status, 128 plus the signal that ended the command, or 127 when the command cannot start; then the output holds the reason.
func RunShell(ctx context.Context, dir, command string) (string, int) {
	cmd := GroupCommand(ctx, shellBinary, []string{"-c", command}, dir)

	output := NewTail(model.ReportLimit)

	cmd.Stdout, cmd.Stderr = output, output

	err := cmd.Run()

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return output.Text(), shellCode(exit)
	}

	if err != nil {
		return err.Error(), exitNotFound
	}

	return output.Text(), 0
}

func shellCode(exit *exec.ExitError) int {
	// The platform wait status is the only place the signal is recorded.
	if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return exitSignalBase + int(status.Signal())
	}

	return exit.ExitCode()
}
