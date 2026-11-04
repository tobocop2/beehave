// Package cli builds the beehave command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	exitOK    = 0
	exitError = 1

	commandName = "beehave"
	shortHelp   = "Take a backlog of issues to reviewed, gated pull requests with any coding agent"
)

// BuildInfo describes the running binary.
type BuildInfo struct {
	Version string
	// Adapters builds the tracker, the forge and the claim store of a project and returns why it cannot. A nil value skips the check.
	Adapters func(context.Context, model.Project) error
	// Items opens the item list of a project with the local tracker, for the item commands and the check of the run. A nil value leaves both out.
	Items ItemOpener
	// Doctor reads what the doctor command checks outside the process: the Keychain and the bd version.
	Doctor Doctor
	// Flow opens the engine and the client of the run, status, answer, resume and cancel commands.
	Flow Flow
	// GateRefusal says why a gate does not run here. A nil value reads as: no sandbox enforcer can run here.
	GateRefusal GateRefusal
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

	root.AddCommand(newCheckCommand(ctx, env, info.Adapters, info.GateRefusal), newDoctorCommand(ctx, env, info.Doctor))
	commands := flowCommands{ctx: ctx, env: env, version: info.Version, factory: info.Flow, items: info.Items}
	commands.add(root)
	commands.addItems(root)

	return root
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
