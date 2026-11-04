// Package gitexec builds the git commands that obee runs in the trees it manages. Every such command runs with the hooks of the repository off, so a hook
// that the owner or the project put in a tree never runs as the owner. This package is the one place that names the git binary.
package gitexec

import (
	"context"
	"os"
	"time"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

// Binary is the name of the git program.
const Binary = "git"

// hooksOff is the setting that points the hooks folder at an empty place.
const hooksOff = "core.hooksPath=" + os.DevNull

// Args returns args with the setting that turns the hooks off in front of them.
func Args(args []string) []string {
	return append([]string{"-c", hooksOff}, args...)
}

// Runner runs git in a folder with the hooks off. Its fields are those of proc.ExecRunner.
type Runner struct {
	proc.ExecRunner
}

// New returns a runner for git in dir. A zero timeout means no limit.
func New(dir string, timeout time.Duration) Runner {
	return Runner{ExecRunner: proc.ExecRunner{Binary: Binary, Dir: dir, Timeout: timeout}}
}

// Run runs git with the hooks off.
func (r Runner) Run(ctx context.Context, args []string) model.CommandResult {
	return r.ExecRunner.Run(ctx, Args(args))
}

// ReadConfig returns the value that the git config of the repository in dir holds for key. It reads configuration and runs no hook, and the hooks setting
// must not reach it, so it does not use Args.
func ReadConfig(ctx context.Context, dir, key string) model.CommandResult {
	return proc.ExecRunner{Binary: Binary, Dir: dir}.Run(ctx, []string{"config", "--get", key})
}
