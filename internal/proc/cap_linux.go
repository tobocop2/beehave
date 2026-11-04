//go:build linux

package proc

import (
	"bytes"
	"context"
	"os/exec"

	"github.com/tobocop2/obee/internal/model"
)

// cappedCommand returns a command that runs the binary in a scope with the cap of the runner, or no command and no error for a binary that is not found,
// so that the plain command reports it.
func (r ExecRunner) cappedCommand(ctx context.Context, args, env []string) (*exec.Cmd, error) {
	target, err := r.resolved()
	if err != nil {
		return nil, err
	}

	found, err := exec.LookPath(target)
	if err != nil {
		return nil, nil //nolint:nilerr // a binary that is not found is reported by the plain command
	}

	return r.hooks.scopedCommand(ctx, found, args, r.Dir, Tree{MaxNewProcesses: r.MaxNewProcesses, Env: env})
}

// run runs cmd to its end and returns its result. The scope fails before the start, so a run has no setup failure to read after it.
func (r ExecRunner) run(ctx context.Context, cmd *exec.Cmd, stdout, stderr *bytes.Buffer) model.CommandResult {
	return r.result(ctx, cmd.Run(), stdout, stderr)
}
