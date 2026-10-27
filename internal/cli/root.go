// Package cli builds the obee command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
)

const (
	exitOK    = 0
	exitError = 1

	commandName = "obee"
	shortHelp   = "Take a backlog of issues to reviewed, gated pull requests with any coding agent"
)

// BuildInfo describes the running binary.
type BuildInfo struct {
	Version string
	// Adapters builds the tracker, the forge and the claim store of a project and returns why it cannot. A nil value skips the check.
	Adapters func(context.Context, model.Project) error
	// Doctor reads what the doctor command checks outside the process: the Keychain and the bd version.
	Doctor Doctor
	// Flow opens the engine and the client of the run, status, answer, resume and cancel commands.
	Flow Flow
	// Check reads what the check commands need outside the process.
	Check CheckDeps
	// Host opens the build host of a project for the host commands. A nil Open leaves them out.
	Host HostDeps
}

// newRootCommand builds the command tree. ctx is the context of the commands that call out. env is the process environment, read once by the caller.
func newRootCommand(ctx context.Context, info BuildInfo, env map[string]string, stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           commandName,
		Short:         shortHelp,
		Version:       info.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	addCommands(ctx, root, info, env)

	return root
}

// addCommands adds the commands of every group to the root.
func addCommands(ctx context.Context, root *cobra.Command, info BuildInfo, env map[string]string) {
	root.AddCommand(
		newCheckCommand(ctx, env, info.Adapters, info.Check), newDoctorCommand(ctx, env, info.Doctor),
		group("brief", "Write and size the brief of a lane", commandsOf(ctx, env, briefSpecs())),
		group("ledger", "Read and write the ledger of lanes", commandsOf(ctx, env, ledgerSpecs(info.Check))),
	)
	flowCommands{ctx: ctx, env: env, version: info.Version, factory: info.Flow}.add(root)

	if specs := hostSpecs(info.Host); len(specs) > 0 {
		root.AddCommand(group("host", "Run jobs on the build host of a project", commandsOf(ctx, env, specs)))
	}
}

// Execute runs the command line and returns the process exit code.
func Execute(ctx context.Context, args []string, info BuildInfo, env map[string]string, stdout, stderr io.Writer) int {
	if err := config.RefuseDBOSVariables(env); err != nil {
		// A failed write to stderr has no recovery.
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", commandName, err)

		return exitError
	}

	root := newRootCommand(ctx, info, env, stdout, stderr)
	root.SetArgs(args)

	if err := root.ExecuteContext(ctx); err != nil {
		var exit *ExitError
		if !errors.As(err, &exit) {
			// A failed write to stderr has no recovery.
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", commandName, err)
		}

		return exitCode(err)
	}

	return exitOK
}
