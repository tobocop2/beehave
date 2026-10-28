package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/hclfile"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

const (
	exitProblems         = 2
	exitWorkflowProblems = 1

	configFlag          = "config"
	projectFlag         = "project"
	defaultWorkflowFile = "workflow.hcl"
)

// ExitError carries the exit code of a command that failed on purpose.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

func newCheckCommand(ctx context.Context, env map[string]string, adapters func(context.Context, model.Project) error, itemFields itemFieldsFunc) *cobra.Command {
	check := &cobra.Command{
		Use:   "check",
		Short: "Check files and settings before a round uses them",
	}
	check.AddCommand(newCheckConfigCommand(ctx, env, adapters), newCheckWorkflowCommand(env, itemFields))

	return check
}

// itemFieldsFunc returns the item fields that the adapters of a project declare.
type itemFieldsFunc func(context.Context, model.Project) ([]string, error)

// workflowCheck holds what `check workflow` reads: the process environment, the adapter hook and the two flags.
type workflowCheck struct {
	env        map[string]string
	itemFields itemFieldsFunc
	project    string
	configDir  string
}

func newCheckWorkflowCommand(env map[string]string, itemFields itemFieldsFunc) *cobra.Command {
	w := &workflowCheck{env: env, itemFields: itemFields}

	command := &cobra.Command{
		Use:   "workflow [file]",
		Short: "Check a workflow file and print one line for each problem",
		Long: "Check the structure, the references, the cycles and the prompt files of a workflow file. " +
			"The default file is " + defaultWorkflowFile + " in the current folder. " +
			"With --project, the item fields come from the tracker and the forge of that project. Without it, the only item fields are id, title and body. " +
			"Exit 0 when the file is valid, 1 when it has problems, 2 when it cannot be read or parsed.",
		Args: cobra.MaximumNArgs(1),
		RunE: w.run,
	}
	command.Flags().StringVar(&w.project, projectFlag, "", "the project whose adapters declare the item fields (default: id, title and body only)")
	command.Flags().StringVar(&w.configDir, configFlag, "", "the config folder (default: $"+config.ConfigDirVariable+", then ~/.config/obee)")

	return command
}

func (w *workflowCheck) run(cmd *cobra.Command, args []string) error {
	file := defaultWorkflowFile
	if len(args) > 0 {
		file = args[0]
	}

	fields, err := w.fields(cmd.Context())
	if err != nil {
		return err
	}

	return runCheckWorkflow(cmd.ErrOrStderr(), file, fields)
}

// fields returns the item fields of the named project. No project name, or a build with no adapter hook, gives the common fields.
func (w *workflowCheck) fields(ctx context.Context) ([]string, error) {
	if w.project == "" {
		return workflow.CommonItemFields(), nil
	}

	project, err := w.loadProject()
	if err != nil {
		return nil, err
	}

	if w.itemFields == nil {
		return workflow.CommonItemFields(), nil
	}

	return w.itemFields(ctx, project)
}

func (w *workflowCheck) loadProject() (model.Project, error) {
	dir, err := config.FolderPath(w.env, w.configDir)
	if err != nil {
		return model.Project{}, err
	}

	cfg, problems := config.LoadFolder(dir, w.env)
	if len(problems) > 0 {
		return model.Project{}, problems
	}

	project, found := cfg.Projects[w.project]
	if !found {
		return model.Project{}, fmt.Errorf("no project named %q. Use one of: %s", w.project, strings.Join(slices.Sorted(maps.Keys(cfg.Projects)), ", "))
	}

	return project.Project, nil
}

func runCheckWorkflow(stderr io.Writer, file string, fields []string) error {
	found, unparsed := workflow.Check(os.DirFS(filepath.Dir(file)), filepath.Base(file), fields)
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
		Long:  "Check runners.hcl and projects/*.hcl. Exit 0 when the folder is valid, 2 when it has problems.",
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
func printProblems(stderr io.Writer, file string, found []hclfile.Diagnostic) {
	for _, problem := range found {
		problem.File = file
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
