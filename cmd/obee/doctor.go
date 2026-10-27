package main

import (
	"context"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/store/beads"
)

// securityTimeout is the longest one call of `security` may run. A locked Keychain can wait for a prompt with no end.
const securityTimeout = 10 * time.Second

// newSecurityRunner returns the runner of the `security` program.
func newSecurityRunner() proc.ExecRunner {
	runner := proc.NewExecRunner("security", "")
	runner.Timeout = securityTimeout

	return runner
}

// newDoctor fills the function values of the doctor command: the bd version through the command line of bd, and the Keychain through `security`.
func newDoctor(bd proc.Runner, security proc.Runner) cli.Doctor {
	return cli.Doctor{
		TokenReadable: func(ctx context.Context, entry string) bool {
			// The token is read to prove the entry holds one. It is never kept or printed.
			done := security.Run(ctx, []string{"find-generic-password", "-s", entry, "-w"})

			return done.ExitCode == 0 && strings.TrimSpace(done.Stdout) != ""
		},
		BdVersion: func(ctx context.Context) error { return beads.RequireBd(ctx, bd) },
	}
}
