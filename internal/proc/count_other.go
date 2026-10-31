//go:build !linux

package proc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// systemPs is the place of ps on macOS. The count does not read the PATH of the request.
const systemPs = "/bin/ps"

// userProcesses returns the processes that the user of this process runs.
func userProcesses() (int, error) {
	return countByPs(systemPs, os.Getuid())
}

// countByPs counts the lines that ps prints for the processes of uid, one process for each line. The count includes the ps that runs it.
func countByPs(ps string, uid int) (int, error) {
	out, err := exec.CommandContext(context.Background(), ps, "-U", strconv.Itoa(uid), "-o", "pid=").Output() //nolint:gosec // fixed binary, the uid is a number
	if err != nil {
		return 0, fmt.Errorf("run %s: %w", ps, err)
	}

	return bytes.Count(out, []byte("\n")), nil
}
