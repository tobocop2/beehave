package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
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

// GateRefusal returns the reason why the gate of a project does not run on this machine, or "". program is true for a gate that names its own program.
// A nil GateRefusal refuses every gate of a project that has its sandbox on and would run here, as if no enforcer could run.
type GateRefusal func(project config.Project, program bool) string

func (f GateRefusal) reason(project config.Project, program bool) string {
	if f != nil {
		return f(project, program)
	}

	if config.GateRefused(project.Project, program) {
		return config.GateSandboxRefusal
	}

	return ""
}

func newCheckCommand(ctx context.Context, env map[string]string, adapters func(context.Context, model.Project) error, refusal GateRefusal) *cobra.Command {
	check := &cobra.Command{
		Use:   "check",
		Short: "Check files and settings before a round uses them",
	}
	check.AddCommand(newCheckConfigCommand(ctx, env, adapters, refusal), newCheckWorkflowCommand(ctx, env, refusal), newCheckCommandCommand())

	return check
}

const (
	dryRunFlag      = "dry-run"
	dryRunItemFlag  = "item"
	dryRunItemValue = "1"
	redGateFlag     = "red-gate"
	sendBackFlag    = "send-back"
)

const checkWorkflowLong = "Check that a workflow file and the files it loads parse, that each name in them is defined, and that the file defines main. " +
	"The default file is " + defaultWorkflowFile + " in the current folder. " +
	"Exit 0 when the file is valid, 1 when it has problems, 2 when it cannot be read or parsed. " +
	"A line that starts with note: is a fact for the author, such as an agent step with sandbox = None. It is no problem and changes no exit code.\n\n" +
	"With --" + dryRunFlag + ", beehave then runs the file for one item over fakes and prints each step with its fake result. " +
	"The run uses a temporary state folder that beehave deletes at the end. It calls no forge, agent, tracker or host. " +
	"Every gate in the run passes. With --" + redGateFlag + ", the first gate is red with one failed step, and each later gate passes, so the run walks the fix round of the workflow. " +
	"A workflow that stops at a red gate exits 1 with --" + redGateFlag + ". " +
	"Every review in the run approves. With --" + sendBackFlag + ", the first review sends the item back, and each later review approves, so the run walks a send-back and a fix round. " +
	"A dry run has three exit codes. " +
	"Exit 0: the run reached the end of the file and every step passed. " +
	"Exit 1: a Starlark error, a REFUSE or a failed faked step; the output shows the error and the last step that ran. " +
	"Exit 3: the run ended early at a step the dry run does not fake, and the steps after it were not checked. " +
	"The last line says what that step does."

// runCheckedWorkflow refuses the calls that the named project cannot make, then checks the file and runs it as runDryWorkflow does.
func runCheckedWorkflow(ctx context.Context, cmd *cobra.Command, env map[string]string, named projectCheck, args []string, item string, dry dryFlags) error {
	file, err := named.fileOf(cmd.ErrOrStderr(), env, args)
	if err != nil {
		return err
	}

	return runDryWorkflow(ctx, cmd, env, file, item, dry)
}

// dryFlags holds the flags of a dry run: whether to run, and which fakes walk a red gate or a send-back.
type dryFlags struct{ run, redGate, sendBack bool }

// runDryWorkflow checks the file, and with the run flag set it then runs the file over fakes and prints the steps. With redGate set, the first gate of the run is red. With sendBack set, the first review sends the item back.
func runDryWorkflow(ctx context.Context, cmd *cobra.Command, env map[string]string, file, item string, dry dryFlags) error {
	if err := runCheckWorkflow(cmd.ErrOrStderr(), file); err != nil || !dry.run {
		return err
	}

	if err := itemID(item); err != nil {
		return err
	}

	var options []flow.DryOption
	if dry.redGate {
		options = append(options, flow.WithRedGate())
	}

	if dry.sendBack {
		options = append(options, flow.WithSendBack())
	}

	return dryExit(flow.DryRun(ctx, file, item, env, cmd.OutOrStdout(), options...))
}

// dryExit turns the error of a dry run into the exit code that the command ends with.
func dryExit(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, flow.ErrDryRunEndedEarly):
		return &ExitError{Code: exitDryRunEndedEarly}
	default:
		return &ExitError{Code: exitWorkflowProblems}
	}
}

func newCheckWorkflowCommand(ctx context.Context, env map[string]string, refusal GateRefusal) *cobra.Command {
	var dry dryFlags

	var item string

	named := projectCheck{refusal: refusal}

	command := &cobra.Command{
		Use:   "workflow [file]",
		Short: "Check a workflow file and print one line for each problem",
		Long:  checkWorkflowLong,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheckedWorkflow(ctx, cmd, env, named, args, item, dry)
		},
	}

	return addDryRunFlags(named.flags(command), &dry, &item)
}

// flags adds --project and --config to the command.
func (p *projectCheck) flags(command *cobra.Command) *cobra.Command {
	command.Flags().StringVar(&p.project, projectFlag, "", "a project of the config: refuse each call of gate, merge, batch_merge or preflight when the project has no repository")
	command.Flags().StringVar(&p.folder, configFlag, "", "the config folder for --"+projectFlag+" (default: $"+config.ConfigDirVariable+", then ~/.config/beehave)")

	return command
}

// projectCheck is the project that `check workflow` judges the calls against, and the config folder that holds it. The project is empty when none is named.
type projectCheck struct {
	project string
	folder  string
	refusal GateRefusal
}

// find loads the config folder and returns the named project. An unreadable config or an unknown name prints its problems and exits with code 2.
func (p projectCheck) find(stderr io.Writer, env map[string]string) (config.Project, error) {
	dir, err := config.FolderPath(env, p.folder)
	if err != nil {
		return config.Project{}, err
	}

	cfg, problems := config.LoadFolder(dir, env)
	if found, ok := cfg.Projects[p.project]; len(problems) == 0 && ok {
		return found, nil
	}

	if len(problems) == 0 {
		problems = config.Problems{unknownProject(p.project, cfg)}
	}

	printLines(stderr, problems)

	return config.Project{}, &ExitError{Code: exitProblems}
}

// fileOf returns the workflow file that the arguments name, or the default file. It first refuses the calls that the named project cannot make.
func (p projectCheck) fileOf(stderr io.Writer, env map[string]string, args []string) (string, error) {
	file := defaultWorkflowFile
	if len(args) > 0 {
		file = args[0]
	}

	return file, p.run(stderr, env, file)
}

// run refuses each call of a builtin that needs a repository when the named project has none. It does nothing when no project is named.
func (p projectCheck) run(stderr io.Writer, env map[string]string, file string) error {
	if p.project == "" {
		return nil
	}

	found, err := p.find(stderr, env)
	if err != nil {
		return err
	}

	calls := p.refusedCalls(found, file)
	if len(calls) == 0 {
		return nil
	}

	printProblems(stderr, file, calls)

	return &ExitError{Code: exitWorkflowProblems}
}

// refusedCalls lists the calls that the named project cannot make: the git builtins of a project with no repository, and the gates that its sandbox refuses on this machine.
func (p projectCheck) refusedCalls(found config.Project, file string) []workflow.Diagnostic {
	var calls []workflow.Diagnostic

	if !found.HasRepository() {
		calls = workflow.CheckNoRepository(file, p.project)
	}

	if reason := p.refusal.reason(found, true); reason != "" {
		calls = append(calls, workflow.CheckSandboxedGates(file, p.project, reason, p.refusal.reason(found, false) != "")...)
	}

	return calls
}

func addDryRunFlags(command *cobra.Command, dry *dryFlags, item *string) *cobra.Command {
	command.Flags().BoolVar(&dry.run, dryRunFlag, false, "after the check, run the file over fakes and print each step")
	command.Flags().BoolVar(&dry.redGate, redGateFlag, false, "with --"+dryRunFlag+", make the first gate red so the run walks the fix round")
	command.Flags().BoolVar(&dry.sendBack, sendBackFlag, false, "with --"+dryRunFlag+", make the first review send the item back so the run walks a fix round")
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

func newCheckConfigCommand(ctx context.Context, env map[string]string, adapters func(context.Context, model.Project) error, refusal GateRefusal) *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "config",
		Short: "Check the config folder and print one line for each problem",
		Long:  "Check runners.star and projects/*.star. Exit 0 when the folder is valid, 2 when it has problems.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheckConfig(ctx, cmd.ErrOrStderr(), env, adapters, refusal, flag)
		},
	}
	addConfigFlag(command, &flag)

	return command
}

func runCheckConfig(ctx context.Context, stderr io.Writer, env map[string]string, adapters func(context.Context, model.Project) error, refusal GateRefusal, flag string) error {
	dir, err := config.FolderPath(env, flag)
	if err != nil {
		return err
	}

	cfg, problems := configProblems(ctx, dir, env, adapters)
	if len(problems) == 0 {
		printHostNotes(stderr, cfg, refusal)

		return nil
	}

	printLines(stderr, problems)

	return &ExitError{Code: exitProblems}
}

// configProblems loads the config folder. If the load reports a problem, it stops there. Otherwise it reports the problems of the adapters and the problems of the schedule workflow files together, because the two checks do not depend on each other.
func configProblems(ctx context.Context, dir string, env map[string]string, adapters func(context.Context, model.Project) error) (config.Config, config.Problems) {
	cfg, problems := config.LoadFolder(dir, env)
	if len(problems) != 0 {
		return cfg, problems
	}

	return cfg, append(adapterProblems(ctx, cfg, adapters), scheduleProblems(cfg)...)
}

// unknownProject is the refusal for a project name that the config does not hold.
func unknownProject(name string, cfg config.Config) error {
	return fmt.Errorf("no project %q in the config: use one of %s", name, strings.Join(slices.Sorted(maps.Keys(cfg.Projects)), ", "))
}

func printLines(stderr io.Writer, problems config.Problems) {
	for _, problem := range problems {
		// A failed write to stderr has no recovery.
		_, _ = fmt.Fprintln(stderr, problem)
	}
}

// printHostNotes prints a note for each project that has the sandbox on. A project on a host runs its gate there with no sandbox.
// A project on this machine cannot run its gate when no sandbox enforcer can run here.
func printHostNotes(stderr io.Writer, cfg config.Config, refusal GateRefusal) {
	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		project := cfg.Projects[name]
		if !project.SandboxByDefault() {
			continue
		}

		// A failed write to stderr has no recovery.
		if project.BuildHost == config.LocalHost {
			if reason := refusal.reason(project, false); reason != "" {
				_, _ = fmt.Fprintf(stderr, "note: project %s has the sandbox on, but %s\n", name, reason)
			}

			continue
		}

		_, _ = fmt.Fprintf(stderr, "note: project %s builds on the host %s, so its gate runs there with no sandbox. The sandbox covers only a gate that runs on this machine.\n", name, project.BuildHost)
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

// scheduleProblems runs the workflow check on the workflow of each schedule, in project name order. It reports each diagnostic with the schedule that names the file.
// The config package checks that the file exists. It cannot parse it, because the workflow package depends on the config package.
func scheduleProblems(cfg config.Config) config.Problems {
	var problems config.Problems

	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		for _, schedule := range cfg.Projects[name].Schedules {
			// A file that cannot be read or parsed always yields a diagnostic (TestCheck_ReportsEachProblemWithItsPlace pins this), so the unparsed flag adds nothing here.
			found, _ := workflow.Check(schedule.Workflow)
			for _, diagnostic := range found {
				problems = append(problems, fmt.Errorf("project %s, schedule %s: %w", name, schedule.Name, diagnostic))
			}
		}
	}

	return problems
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
