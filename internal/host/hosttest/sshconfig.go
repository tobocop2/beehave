package hosttest

import (
	"context"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc"
)

// CleanSSHConfig is an `ssh -G` answer that gives the host neither the agent nor the X display of the owner.
const CleanSSHConfig = "forwardagent no\nforwardx11 no\nforwardx11trusted no\n"

// SSHConfig wraps the ssh runner of a fake host. It answers `ssh -G` with Answer and counts it in Asked. Every other call goes to Next.
// A provider test uses it to give a host declared credentials = "none" the ssh config of the owner that the test needs.
type SSHConfig struct {
	Answer string
	Next   proc.Runner
	Asked  int
}

// Run answers `ssh -G` and passes every other call on.
func (c *SSHConfig) Run(ctx context.Context, args []string) model.CommandResult {
	if args[0] == "-G" {
		c.Asked++

		return model.CommandResult{Stdout: c.Answer}
	}

	return c.Next.Run(ctx, args)
}
