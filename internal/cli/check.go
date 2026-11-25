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

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/ci"
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

func newCheckCommand(ctx context.Context, env map[string]string, adapters func(context.Context, model.Project) error, registry config.Adapters, refusal GateRefusal, gateList ci.Reader, hooks map[model.Agent]agent.HookDecoder) *cobra.Command {
	check := &cobra.Command{
		Use:   "check",
		Short: "Check files and settings before a round uses them",
	}
	check.AddCommand(newCheckConfigCommand(ctx, env, adapters, registry, refusal), newCheckWorkflowCommand(ctx, env, registry, refusal), newCheckCommandCommand(hooks))

	if gateList != nil {
		check.AddCommand(newCheckGateListCommand(gateList))
	}

	return check
}

const (
	dryRunFlag      = "dry-run"
	dryRunItemFlag  = "item"
	dryRunItemValue = "1"
	fixtureFlag     = "fixture"
)

const checkWorkflowLong = "Check that a workflow file and the files it loads parse, that each name in them is defined, and that the file defines main. " +
	"The default file is " + defaultWorkflowFile + " in the current folder. " +
	"Exit 0 when the file is valid, 1 when it has problems, 2 when it cannot be read or parsed. " +
	"A line that starts with note: is a fact for the author, such as an agent step with sandbox = None. It is no problem and changes no exit code.\n\n" +
	"With --" + dryRunFlag + ", beehave then runs the file for one item over fakes and prints each step with its fake result. " +
	"The run uses a temporary state folder that beehave deletes at the end. It calls no forge, agent, tracker or host. " +
	"Every gate in the run passes, and every review approves. " +
	"A dry run takes no real time: a sleep reports 0 seconds, and a wait or a recv with no entry in the fixture times out at once. A wait with no timeout and no entry is refused, because a real run would wait for ever. " +
	"With --" + fixtureFlag + " <file.json>, the file scripts the result of each agent, gate, wait and recv call by key, so a review can reject twice and then approve, or a gate can be red once. " +
	"A key is agent, gate, wait or recv, or agent:<label>, gate:<label>, wait:<label> or recv:<label> for the call with that name. A key holds a list with one result for each call of that key, in order, or one object that every call of that key takes. " +
	"An agent result is {\"outputs\": {\"<name>\": \"<text>\"}}; an output that the result does not name keeps its base value. " +
	"A gate result is {\"passed\": false}, which is a red gate with one failed step, or {\"passed\": true}. " +
	"A wait result is {\"answer\": \"<text>\", \"timed_out\": <bool>}. A recv result is {\"value\": <JSON>, \"timed_out\": <bool>}. " +
	"With a fixture, an agent call or a gate call whose key the file does not list fails the run, so does a call past the end of a list, and so does a result that no call used. " +
	"The gate of a merge has no key and always passes. " +
	"The file must be valid: an unknown key or field, or a value of the wrong type, exits 2 and names the file. " +
	"A dry run has three exit codes. " +
	"Exit 0: the run reached the end of the file and every step passed. " +
	"Exit 1: a Starlark error, a REFUSE or a failed faked step; the output shows the error and the last step that ran. " +
	"Exit 3: the run ended early at a step the dry run does not fake, and the steps after it were not checked. " +
	"The last line says what that step does. " +
	"With --" + projectFlag + ", the dry run reads the vars and the max_spawn_depth of the project, and the runners of the config: " +
	"a step on a runner whose agent has no skill-load line is refused, as in a real run."

// runCheckedWorkflow refuses the calls that the named project cannot make, then checks the file and runs it as runDryWorkflow does.
func runCheckedWorkflow(ctx context.Context, cmd *cobra.Command, env map[string]string, named projectCheck, args []string, item string, dry dryFlags) error {
	file, found, err := named.fileOf(cmd.ErrOrStderr(), env, args)
	if err != nil {
		return err
	}

	return runDryWorkflow(ctx, cmd, env, file, item, dry, found.Project)
}

// dryFlags holds the flags of a dry run: whether to run, and the fixture file that scripts the agent calls and the gate calls.
type dryFlags struct {
	run     bool
	fixture string
}

// runDryWorkflow checks the file, and with the run flag set it then runs the file over fakes and prints the steps. With a fixture file, the file scripts the agent calls and the gate calls.
// The facts of the run hold the vars and the max_spawn_depth of the named project. They are static config, so the dry run reads them.
func runDryWorkflow(ctx context.Context, cmd *cobra.Command, env map[string]string, file, item string, dry dryFlags, project model.Project) error {
	if err := runCheckWorkflow(cmd.ErrOrStderr(), file); err != nil {
		return err
	}

	if !dry.run {
		return refuseFixtureWithoutDryRun(cmd.ErrOrStderr(), dry)
	}

	if err := itemID(item); err != nil {
		return err
	}

	options, err := dryOptions(cmd.ErrOrStderr(), dry, project)
	if err != nil {
		return err
	}

	return dryExit(flow.DryRun(ctx, file, item, env, cmd.OutOrStdout(), options...))
}

// dryOptions turns the flags and the project of a dry run into its options. A fixture file that cannot be loaded prints its error and exits 2.
func dryOptions(stderr io.Writer, dry dryFlags, project model.Project) ([]flow.DryOption, error) {
	options := []flow.DryOption{flow.WithVars(project.Vars), flow.WithSpawnDepth(project.MaxSpawnDepth), flow.WithRunnerSkillLoads(project.RunnerSkillLoads), flow.WithSandbox(project.RunSandboxMode())}
	if dry.fixture == "" {
		return options, nil
	}

	fixture, err := flow.LoadFixture(dry.fixture)
	if err != nil {
		// A failed write to stderr has no recovery.
		_, _ = fmt.Fprintf(stderr, "%v\n", err)

		return nil, &ExitError{Code: exitProblems}
	}

	return append(options, flow.WithFixture(fixture)), nil
}

// refuseFixtureWithoutDryRun refuses a fixture file on a check that runs nothing: the file has no use there.
func refuseFixtureWithoutDryRun(stderr io.Writer, dry dryFlags) error {
	if dry.fixture == "" {
		return nil
	}

	// A failed write to stderr has no recovery.
	_, _ = fmt.Fprintf(stderr, "--%s needs --%s\n", fixtureFlag, dryRunFlag)

	return &ExitError{Code: exitProblems}
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

func newCheckWorkflowCommand(ctx context.Context, env map[string]string, registry config.Adapters, refusal GateRefusal) *cobra.Command {
	var dry dryFlags

	var item string

	named := projectCheck{refusal: refusal, registry: registry}

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
	command.Flags().StringVar(&p.project, projectFlag, "", "a project of the config: refuse each call of gate, merge, preflight, pr_read or issue_assignees when the project has no repository")
	command.Flags().StringVar(&p.folder, configFlag, "", "the config folder for --"+projectFlag+" (default: $"+config.ConfigDirVariable+", then ~/.config/beehave)")

	return command
}

// projectCheck is the project that `check workflow` judges the calls against, and the config folder that holds it. The project is empty when none is named.
type projectCheck struct {
	project  string
	folder   string
	refusal  GateRefusal
	registry config.Adapters
}

// find loads the config folder and returns the named project. An unreadable config or an unknown name prints its problems and exits with code 2.
func (p projectCheck) find(stderr io.Writer, env map[string]string) (config.Project, error) {
	dir, err := config.FolderPath(env, p.folder)
	if err != nil {
		return config.Project{}, err
	}

	cfg, problems := config.LoadFolder(dir, env, p.registry)
	if found, ok := cfg.Projects[p.project]; len(problems) == 0 && ok {
		found.RunnerSkillLoads = p.runnerSkillLoads(cfg.Runners)

		return found, nil
	}

	if len(problems) == 0 {
		problems = config.Problems{unknownProject(p.project, cfg)}
	}

	printLines(stderr, problems)

	return config.Project{}, &ExitError{Code: exitProblems}
}

// runnerSkillLoads says for each runner of the config whether its agent CLI has a line that loads a skill before the first turn, as the facts of a real run say.
// The loader of the config refuses a runner whose agent the registry lacks, so each runner here has an entry.
func (p projectCheck) runnerSkillLoads(runners []model.Runner) map[string]bool {
	loads := make(map[string]bool, len(runners))

	for _, runner := range runners {
		loads[runner.Name] = p.registry.Agents[runner.Agent].LoadsSkills
	}

	return loads
}

// fileOf returns the workflow file that the arguments name, or the default file. It first refuses the calls that the named project cannot make.
// It also returns the named project, which is empty when no project is named.
func (p projectCheck) fileOf(stderr io.Writer, env map[string]string, args []string) (string, config.Project, error) {
	file := defaultWorkflowFile
	if len(args) > 0 {
		file = args[0]
	}

	found, err := p.run(stderr, env, file)

	return file, found, err
}

// run refuses each call of a builtin that needs a repository when the named project has none. It does nothing when no project is named.
// It returns the named project.
func (p projectCheck) run(stderr io.Writer, env map[string]string, file string) (config.Project, error) {
	if p.project == "" {
		return config.Project{}, nil
	}

	found, err := p.find(stderr, env)
	if err != nil {
		return config.Project{}, err
	}

	calls := p.refusedCalls(found, file)
	if len(calls) == 0 {
		return found, nil
	}

	printProblems(stderr, file, calls)

	return found, &ExitError{Code: exitWorkflowProblems}
}

// refusedCalls lists the calls that the named project cannot make: the git builtins of a project with no repository, a step with sandbox = None in a project with the sandbox on, and the gates that its sandbox refuses on this machine.
func (p projectCheck) refusedCalls(found config.Project, file string) []workflow.Diagnostic {
	var calls []workflow.Diagnostic

	if !found.HasRepository() {
		calls = workflow.CheckNoRepository(file, p.project)
	}

	calls = append(calls, workflow.CheckSandboxNone(file, p.project, found.RunSandboxMode())...)

	if reason := p.refusal.reason(found, true); reason != "" {
		calls = append(calls, workflow.CheckSandboxedGates(file, p.project, reason, p.refusal.reason(found, false) != "")...)
	}

	return calls
}

func addDryRunFlags(command *cobra.Command, dry *dryFlags, item *string) *cobra.Command {
	command.Flags().BoolVar(&dry.run, dryRunFlag, false, "after the check, run the file over fakes and print each step")
	command.Flags().StringVar(&dry.fixture, fixtureFlag, "", "with --"+dryRunFlag+", a JSON file that scripts the result of each agent call and gate call by key, such as a review that rejects twice or a gate that is red once")
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

func newCheckConfigCommand(ctx context.Context, env map[string]string, adapters func(context.Context, model.Project) error, registry config.Adapters, refusal GateRefusal) *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "config",
		Short: "Check the config folder and print one line for each problem",
		Long:  "Check runners.star and projects/*.star. Exit 0 when the folder is valid, 2 when it has problems.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheckConfig(ctx, cmd.ErrOrStderr(), env, adapters, registry, refusal, flag)
		},
	}
	addConfigFlag(command, &flag)

	return command
}

func runCheckConfig(ctx context.Context, stderr io.Writer, env map[string]string, adapters func(context.Context, model.Project) error, registry config.Adapters, refusal GateRefusal, flag string) error {
	dir, err := config.FolderPath(env, flag)
	if err != nil {
		return err
	}

	cfg, problems := configProblems(ctx, dir, env, adapters, registry)
	if len(problems) == 0 {
		printDefaultNotes(stderr, cfg)
		printHostNotes(stderr, cfg, refusal)

		return nil
	}

	printLines(stderr, problems)

	return &ExitError{Code: exitProblems}
}

// configProblems loads the config folder. If the load reports a problem, it stops there. Otherwise it reports the problems of the adapters.
func configProblems(ctx context.Context, dir string, env map[string]string, adapters func(context.Context, model.Project) error, registry config.Adapters) (config.Config, config.Problems) {
	cfg, problems := config.LoadFolder(dir, env, registry)
	if len(problems) != 0 {
		return cfg, problems
	}

	return cfg, adapterProblems(ctx, cfg, adapters)
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

// printDefaultNotes prints a note for each project whose file sets no sandbox key, so that the default it runs with is visible.
func printDefaultNotes(stderr io.Writer, cfg config.Config) {
	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		if project := cfg.Projects[name]; project.SandboxUnset {
			// A failed write to stderr has no recovery.
			_, _ = fmt.Fprintf(stderr, "note: project %s sets no sandbox key, so its agent steps run in the sandbox. To run them with no sandbox, write sandbox = \"off\" in %s.\n", name, project.Path)
		}
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

// adapterProblems builds the adapters each project names, in name order, and reports each project that cannot build them.
func adapterProblems(ctx context.Context, cfg config.Config, adapters func(context.Context, model.Project) error) config.Problems {
	var problems config.Problems

	if adapters == nil {
		return problems
	}

	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		project := cfg.Projects[name]
		if err := adapters(ctx, project.Project); err != nil {
			// A refusal of a block already holds the file and the position of the call, so the file is not named a second time.
			if !strings.Contains(err.Error(), project.Path) {
				err = fmt.Errorf("%s: %w", project.Path, err)
			}

			problems = append(problems, err)
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
