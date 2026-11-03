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
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

const (
	exitProblems         = 2
	exitWorkflowProblems = 1
	exitDryRunEndedEarly = 3

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
	check.AddCommand(newCheckConfigCommand(ctx, env, adapters), newCheckWorkflowCommand(ctx, env), newCheckCommandCommand())

	return check
}

const (
	dryRunFlag      = "dry-run"
	dryRunItemFlag  = "item"
	dryRunItemValue = "1"
)

const checkWorkflowLong = "Check that a workflow file and the files it loads parse, that each name in them is defined, and that the file defines main. " +
	"The default file is " + defaultWorkflowFile + " in the current folder. " +
	"Exit 0 when the file is valid, 1 when it has problems, 2 when it cannot be read or parsed. " +
	"A line that starts with note: is a fact for the author, such as an agent step with sandbox = None. It is no problem and changes no exit code.\n\n" +
	"With --" + dryRunFlag + ", obee then runs the file for one item over fakes and prints each step with its fake result. " +
	"The run uses a temporary state folder that obee deletes at the end. It calls no forge, agent, tracker or host. " +
	"A dry run has three exit codes. " +
	"Exit 0: the run reached the end of the file and every step passed. " +
	"Exit 1: a Starlark error, a REFUSE or a failed faked step; the output shows the error and the last step that ran. " +
	"Exit 3: the run ended early at a step the dry run does not fake, and the steps after it were not checked. " +
	"The last line says what that step does."

// runDryWorkflow checks the file, and with dryRun set it then runs the file over fakes and prints the steps.
func runDryWorkflow(ctx context.Context, cmd *cobra.Command, env map[string]string, file, item string, dryRun bool) error {
	if err := runCheckWorkflow(cmd.ErrOrStderr(), file); err != nil || !dryRun {
		return err
	}

	if err := itemID(item); err != nil {
		return err
	}

	if err := flow.DryRun(ctx, file, item, env, cmd.OutOrStdout()); err != nil {
		if errors.Is(err, flow.ErrDryRunEndedEarly) {
			return &ExitError{Code: exitDryRunEndedEarly}
		}

		return &ExitError{Code: exitWorkflowProblems}
	}

	return nil
}

func newCheckWorkflowCommand(ctx context.Context, env map[string]string) *cobra.Command {
	var dryRun bool

	var item string

	command := &cobra.Command{
		Use:   "workflow [file]",
		Short: "Check a workflow file and print one line for each problem",
		Long:  checkWorkflowLong,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file := defaultWorkflowFile
			if len(args) > 0 {
				file = args[0]
			}

			return runDryWorkflow(ctx, cmd, env, file, item, dryRun)
		},
	}

	return addDryRunFlags(command, &dryRun, &item)
}

func addDryRunFlags(command *cobra.Command, dryRun *bool, item *string) *cobra.Command {
	command.Flags().BoolVar(dryRun, dryRunFlag, false, "after the check, run the file over fakes and print each step")
	command.Flags().StringVar(item, dryRunItemFlag, dryRunItemValue, "the item id for --"+dryRunFlag)

	return command
}

func runCheckWorkflow(stderr io.Writer, file string) error {
	found, unparsed := workflow.Check(file)

	for _, note := range workflow.Notes(file) {
		// A failed write to stderr has no recovery.
		_, _ = fmt.Fprintf(stderr, "note: %s\n", note)
	}

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

	if len(problems) == 0 {
		printHostNotes(stderr, cfg)

		return nil
	}

	printLines(stderr, problems)

	return &ExitError{Code: exitProblems}
}

func printLines(stderr io.Writer, problems config.Problems) {
	for _, problem := range problems {
		// A failed write to stderr has no recovery.
		_, _ = fmt.Fprintln(stderr, problem)
	}
}

// printHostNotes prints a note for each project that has the sandbox on and builds on a host, because its gate runs there with no sandbox.
func printHostNotes(stderr io.Writer, cfg config.Config) {
	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		if project := cfg.Projects[name]; project.SandboxByDefault() && project.BuildHost != config.LocalHost {
			// A failed write to stderr has no recovery.
			_, _ = fmt.Fprintf(stderr, "note: project %s builds on the host %s, so its gate runs there with no sandbox. The sandbox covers only a gate that runs on this machine.\n", name, project.BuildHost)
		}
	}
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
