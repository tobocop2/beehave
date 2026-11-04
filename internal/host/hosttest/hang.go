package hosttest

import (
	"context"
	"strings"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

// HangingGit wraps the ssh runner of a fake host. The host holds one repository, at repo. Its find lists that repository,
// and its git never answers a ref listing until the context ends. Every other command goes to inner.
// A provider test uses it to prove that the provider passes `host.bank_budget` to the bank scan.
func HangingGit(inner proc.Runner, repo string) proc.Runner { //nolint:ireturn // the callers pass it where a proc.Runner is taken
	return hanging{inner: inner, repo: repo}
}

type hanging struct {
	inner proc.Runner
	repo  string
}

func (h hanging) Run(ctx context.Context, args []string) model.CommandResult {
	line := strings.Join(args, " ")

	switch {
	case strings.Contains(line, "for-each-ref"):
		<-ctx.Done()

		return model.CommandResult{ExitCode: -1, Stderr: ctx.Err().Error()}
	case strings.Contains(line, "find"):
		return model.CommandResult{Stdout: h.repo + "/.git\x00"}
	}

	return h.inner.Run(ctx, args)
}
