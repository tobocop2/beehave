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
	workflowFlag        = "workflow"
	defaultWorkflowFile = "workflow.hcl"
)

// ExitError carries the exit code of a command that failed on purpose.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

func newCheckCommand(ctx context.Context, env map[string]string, adapters func(context.Context, model.Project) error, capabilities capabilitiesFunc) *cobra.Command {
	check := &cobra.Command{
		Use:   "check",
		Short: "Check files and settings before a round uses them",
	}
	check.AddCommand(newCheckConfigCommand(ctx, env, adapters), newCheckWorkflowCommand(env, capabilities))

	return check
}

// capabilitiesFunc returns what the adapters of a project offer to a workflow: the item fields and whether a forge exists.
type capabilitiesFunc func(context.Context, model.Project) (workflow.Capabilities, error)

// projectCapabilities asks the hook for the capabilities of the project. A build with no hook offers the common item fields, and a forge when the project names one.
func projectCapabilities(ctx context.Context, hook capabilitiesFunc, project model.Project) (workflow.Capabilities, error) {
	if hook == nil {
		return workflow.Capabilities{Project: project.Name, Fields: workflow.CommonItemFields(), Forge: project.Forge != ""}, nil
	}

	return hook(ctx, project)
}

// workflowCheck holds what `check workflow` reads: the process environment, the adapter hook and the two flags.
type workflowCheck struct {
	env          map[string]string
	capabilities capabilitiesFunc
	project      string
	configDir    string
}

func newCheckWorkflowCommand(env map[string]string, capabilities capabilitiesFunc) *cobra.Command {
	w := &workflowCheck{env: env, capabilities: capabilities}

	command := &cobra.Command{
		Use:   "workflow [file]",
		Short: "Check a workflow file and print one line for each problem",
		Long: "Check the structure, the references, the cycles and the prompt files of a workflow file. " +
			"The default file is " + defaultWorkflowFile + " in the current folder. " +
			"With --project, the item fields come from the tracker and the forge of that project, and a step that needs a forge is refused when the project has none. Without it, the only item fields are id, title and body. " +
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

	caps, err := w.offered(cmd.Context())
	if err != nil {
		return err
	}

	return runCheckWorkflow(cmd.ErrOrStderr(), file, caps)
}

// offered returns the capabilities of the named project. With no project name, the check knows only the common item fields and cannot tell whether a forge exists, so it refuses no step.
func (w *workflowCheck) offered(ctx context.Context) (workflow.Capabilities, error) {
	if w.project == "" {
		return workflow.Capabilities{Fields: workflow.CommonItemFields(), Forge: true}, nil
	}

	project, err := w.loadProject()
	if err != nil {
		return workflow.Capabilities{}, err
	}

	return projectCapabilities(ctx, w.capabilities, project)
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

func runCheckWorkflow(stderr io.Writer, file string, caps workflow.Capabilities) error {
	found, unparsed := workflow.Check(os.DirFS(filepath.Dir(file)), filepath.Base(file), caps)
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
