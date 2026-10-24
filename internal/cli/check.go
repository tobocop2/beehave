package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/tobocop2/obee/internal/config"
)

const (
	exitProblems = 2

	configFlag = "config"
)

// ExitError carries the exit code of a command that failed on purpose.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

func newCheckCommand(env map[string]string) *cobra.Command {
	check := &cobra.Command{
		Use:   "check",
		Short: "Check files and settings before a round uses them",
	}
	check.AddCommand(newCheckConfigCommand(env))

	return check
}

func newCheckConfigCommand(env map[string]string) *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "config",
		Short: "Check the config folder and print one line for each problem",
		Long:  "Check runners.toml and projects/*.toml. Exit 0 when the folder is valid, 2 when it has problems.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheckConfig(cmd.ErrOrStderr(), env, flag)
		},
	}
	command.Flags().StringVar(&flag, configFlag, "", "the config folder (default: $"+config.ConfigDirVariable+", then ~/.config/obee)")

	return command
}

func runCheckConfig(stderr io.Writer, env map[string]string, flag string) error {
	dir, err := config.FolderPath(env, flag)
	if err != nil {
		return err
	}

	_, problems := config.LoadFolder(dir, env)
	for _, problem := range problems {
		// A failed write to stderr has no recovery.
		_, _ = fmt.Fprintln(stderr, problem)
	}

	if len(problems) > 0 {
		return &ExitError{Code: exitProblems}
	}

	return nil
}

func exitCode(err error) int {
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}

	return exitError
}
