// Package cli builds the obee command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/tobocop2/obee/internal/config"
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
}

// NewRootCommand builds the command tree. env is the process environment, read once by the caller.
func NewRootCommand(info BuildInfo, env map[string]string, stdout, stderr io.Writer) *cobra.Command {
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

	root.AddCommand(newCheckCommand(env))

	return root
}

// Execute runs the command line and returns the process exit code.
func Execute(ctx context.Context, args []string, info BuildInfo, env map[string]string, stdout, stderr io.Writer) int {
	if err := config.RefuseDBOSVariables(env); err != nil {
		// A failed write to stderr has no recovery.
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", commandName, err)

		return exitError
	}

	root := NewRootCommand(info, env, stdout, stderr)
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
