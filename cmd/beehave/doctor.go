package main

import (
	"context"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/proc"
)

// securityTimeout is the longest one call of `security` may run. A locked Keychain can wait for a prompt with no end.
const securityTimeout = 10 * time.Second

// newSecurityRunner returns the runner of the `security` program.
func newSecurityRunner() proc.ExecRunner {
	runner := proc.NewExecRunner("security", "")
	runner.Timeout = securityTimeout

	return runner
}

// newDoctor composes the doctor from the adapter table: the Keychain through `security`, and the tool versions, logins and fix texts that the entries declare.
func newDoctor(table adapters, security proc.Runner) cli.Doctor {
	return cli.Doctor{
		TokenReadable: func(ctx context.Context, entry string) bool {
			// The token is read to prove the entry holds one. It is never kept or printed.
			done := security.Run(ctx, []string{"find-generic-password", "-s", entry, "-w"})

			return done.ExitCode == 0 && strings.TrimSpace(done.Stdout) != ""
		},
		ToolVersions:   table.toolVersions(),
		Logins:         table.logins(),
		ConfigDirFixes: table.configFixes(),
		TokenFixes:     table.tokenFixes(),
	}
}
