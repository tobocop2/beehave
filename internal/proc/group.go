package proc

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// ErrNoProcess is returned by StartTime when no process has the given id.
var ErrNoProcess = errors.New("no such process")

// KillGroupID sends SIGKILL to the process group with the given id. It fails with ESRCH when no member is left.
func KillGroupID(pgid int) error {
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

// StartTime returns the start time of a process as ps prints it, so two processes that reuse one id differ. A missing process reads as ErrNoProcess.
func StartTime(ctx context.Context, pid int) (string, error) {
	cmd := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)) //nolint:gosec // fixed binary name, the id is an integer
	cmd.Env = []string{"LC_ALL=C"}

	out, err := cmd.Output()

	started := strings.TrimSpace(string(out))
	if started == "" {
		return "", errors.Join(ErrNoProcess, err)
	}

	return started, nil
}
