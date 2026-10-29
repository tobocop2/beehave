package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/spf13/cobra"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

const (
	exitProblems         = 2
	exitWorkflowProblems = 1

	configFlag          = "config"
	projectFlag         = "project"
	workflowFlag        = "workflow"
	defaultWorkflowFile = "workflow.star"
)

// ExitError carries the exit code of a command that failed on purpose.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

func newCheckCommand(ctx context.Context, env map[string]string, adapters func(context.Context, model.Project) error) *cobra.Command {
	check := &cobra.Command{
		Use:   "check",
		Short: "Check files and settings before a round uses them",
	}
	check.AddCommand(newCheckConfigCommand(ctx, env, adapters), newCheckWorkflowCommand())

	return check
}

func newCheckWorkflowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "workflow [file]",
		Short: "Check a workflow file and print one line for each problem",
		Long: "Check that a workflow file and the files it loads parse, that each name in them is defined, and that the file defines main. " +
			"The default file is " + defaultWorkflowFile + " in the current folder. " +
			"Exit 0 when the file is valid, 1 when it has problems, 2 when it cannot be read or parsed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file := defaultWorkflowFile
			if len(args) > 0 {
				file = args[0]
			}

			return runCheckWorkflow(cmd.ErrOrStderr(), file)
		},
	}
}

func runCheckWorkflow(stderr io.Writer, file string) error {
	found, unparsed := workflow.Check(file)
	if len(found) == 0 {
		return nil
	}

	printProblems(stderr, file, found)

	if unparsed {
		return &ExitError{Code: exitProblems}
	}

	return &ExitError{Code: exitWorkflowProblems}
}

func newCheckConfigCommand(ctx context.Context, env map[string]string, adapters func(context.Context, model.Project) error) *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "config",
		Short: "Check the config folder and print one line for each problem",
		Long:  "Check runners.star and projects/*.star. Exit 0 when the folder is valid, 2 when it has problems.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheckConfig(ctx, cmd.ErrOrStderr(), env, adapters, flag)
		},
	}
	command.Flags().StringVar(&flag, configFlag, "", "the config folder (default: $"+config.ConfigDirVariable+", then ~/.config/obee)")

	return command
}

func runCheckConfig(ctx context.Context, stderr io.Writer, env map[string]string, adapters func(context.Context, model.Project) error, flag string) error {
	dir, err := config.FolderPath(env, flag)
	if err != nil {
		return err
	}

	cfg, problems := config.LoadFolder(dir, env)
	if len(problems) == 0 {
		problems = adapterProblems(ctx, cfg, adapters)
	}

	for _, problem := range problems {
		// A failed write to stderr has no recovery.
		_, _ = fmt.Fprintln(stderr, problem)
	}

	if len(problems) > 0 {
		return &ExitError{Code: exitProblems}
	}

	return nil
}

// printProblems prints one line for each problem and then the count. The check reads the file by
// its name inside its folder, and each line shows the path that the user typed.
func printProblems(stderr io.Writer, file string, found []workflow.Diagnostic) {
	for _, problem := range found {
		// A failed write to stderr has no recovery.
		_, _ = fmt.Fprintln(stderr, problem)
	}

	noun := "problems"
	if len(found) == 1 {
		noun = "problem"
	}

	// A failed write to stderr has no recovery.
	_, _ = fmt.Fprintf(stderr, "%d %s in %s\n", len(found), noun, file)
}

// adapterProblems builds the adapters each project names, in name order, and reports each project that cannot build them.
func adapterProblems(ctx context.Context, cfg config.Config, adapters func(context.Context, model.Project) error) config.Problems {
	var problems config.Problems

	if adapters == nil {
		return problems
	}

	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		project := cfg.Projects[name]
		if err := adapters(ctx, project.Project); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", project.Path, err))
		}
	}

	return problems
}

func exitCode(err error) int {
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}

	return exitError
}
