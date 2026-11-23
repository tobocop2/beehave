// Package remote holds the git runner that reaches a remote. Only the package reach imports it, so no other code can build such a runner.
package remote

import (
	"context"
	"time"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec/internal/gitbin"
)

// Runner runs git with the hooks off and allows every transport. Its fields are those of proc.ExecRunner.
type Runner struct {
	proc.ExecRunner
}

// New returns a runner for git in dir. Run it only where no agent writes the git config: in a repository that only the engine writes, with --git-dir set,
// or for a clone into a folder that does not exist yet.
func New(dir string, timeout time.Duration) Runner {
	return Runner{ExecRunner: proc.ExecRunner{Binary: gitbin.Name, Dir: dir, Timeout: timeout}}
}

// Run runs git with the hooks off.
func (r Runner) Run(ctx context.Context, args []string) model.CommandResult {
	return r.ExecRunner.Run(ctx, gitbin.Args(args))
}
