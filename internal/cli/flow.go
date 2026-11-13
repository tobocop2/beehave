package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	// maxRunArgs is the item and the optional project.
	maxRunArgs = 2
	// overCapFlag is the flag of beehave run that lets the first agent step take a runner over its usage cap.
	overCapFlag = "over-cap"
)

// errNoWorkflow is returned by run when no workflow file is named and the current folder holds none.
var errNoWorkflow = errors.New("the run has no workflow file")

// errInterrupted is returned by run when the user stops the command before the item ends.
var errInterrupted = errors.New("stopped before the item finished: run beehave run again to continue it")

// Runner is the engine that the run command drives. Call Launch, then Run, then Shutdown.
type Runner interface {
	Launch() error
	RunFile(item, project, file string) (flow.ItemResult, error)
	Shutdown() error
}

// Reading reads runs, their steps and their lanes without changing any.
type Reading interface {
	Status() ([]flow.RunInfo, error)
	// Steps lists the steps of one run, in order.
	Steps(id string) (flow.RunSteps, error)
	// Lanes lists the lanes of one item across all its runs, oldest attempt first.
	Lanes(item string) (flow.ItemLanes, error)
	// Poll returns the newest review poll that finished. The second value is false when none has.
	Poll() (flow.PollOutput, bool, error)
	// PollNotScheduled returns why the last launch could not schedule the review poll. It is empty when the poll is scheduled.
	PollNotScheduled() (string, error)
	// Schedules lists each schedule with its last tick and whether an engine fires it.
	Schedules() ([]flow.ScheduleInfo, error)
	// Days sums the runs that started or ended since the given time by local day, in the time zone of that time.
	Days(since time.Time) ([]flow.DayFigures, error)
	// PinnedFile returns the workflow file of the open run of the item, or an empty name.
	PinnedFile(item string) (string, error)
}

// Steering reads and steers runs without running any.
type Steering interface {
	Reading
	// Answer sends a plain answer. AnswerAs sends the answer of a named person, which a wait that names people needs.
	Answer(item, text string) error
	AnswerAs(item, from, text string) error
	Resume(item string) error
	// EngineRuns reports whether an engine holds the state folder.
	EngineRuns() (bool, error)
	// Engine reports whether an engine holds the state folder, and what that engine says about itself.
	Engine() (flow.EngineInfo, bool, error)
	// JoinRun gives the item to the engine that holds the state folder and waits for its result.
	JoinRun(ctx context.Context, item, project, file string) (flow.ItemResult, error)
	// ResumeOnThisBuild moves a run that another build started to this build, from its last finished step.
	ResumeOnThisBuild(item string) error
	Cancel(item string) error
	Close() error
}

// Flow opens the engine and the client over the validated config. A nil factory leaves its commands out.
type Flow struct {
	Engine func(context.Context, config.Config, flow.Options) (Runner, error)
	Client func(context.Context, config.Config, flow.Options) (Steering, error)
	// Now is the clock that status counts the time left to a deadline against. A nil value reads the system clock.
	Now func() time.Time
}

// flowCommands holds what the five commands share: the process environment, the build version and the factories.
type flowCommands struct {
	ctx      context.Context //nolint:containedctx // cobra gives a command no context of its own before it runs; the root passes it in
	env      map[string]string
	version  string
	factory  Flow
	items    map[model.TrackerName]ItemOpener
	registry config.Adapters
}

func (f flowCommands) add(root *cobra.Command) {
	if f.factory.Client == nil {
		return
	}

	root.AddCommand(f.statusCommand(), f.answerCommand(), f.resumeCommand(), f.cancelCommand())

	if f.factory.Engine != nil {
		root.AddCommand(f.runCommand(), f.serveCommand())
	}
}

// options loads and validates the config folder, then builds the options of the engine and the client.
// Every command passes here, so each refuses the same unusable settings.
func (f flowCommands) options(stderr io.Writer, flag string) (config.Config, flow.Options, error) {
	dir, err := config.FolderPath(f.env, flag)
	if err != nil {
		return config.Config{}, flow.Options{}, err
	}

	cfg, problems := config.LoadFolder(dir, f.env, f.registry)
	if len(problems) > 0 {
		printLines(stderr, problems)

		return cfg, flow.Options{}, &ExitError{Code: exitProblems}
	}

	state, err := config.StateDir(f.env)
	if err != nil {
		return cfg, flow.Options{}, err
	}

	return cfg, f.engineOptions(cfg, state), nil
}

// engineOptions builds the options of the engine from the config and the state folder.
func (f flowCommands) engineOptions(cfg config.Config, state string) flow.Options {
	return flow.Options{
		StateDir: state, Version: f.version, MaxConcurrent: cfg.Agents.MaxConcurrent, MaxReviewConcurrent: cfg.Agents.MaxReviewConcurrent, Env: f.env,
		MaxRunBudget: cfg.Agents.MaxRunBudgetUSD,
		ReviewPoll:   time.Duration(cfg.Agents.ReviewPollMinutes) * time.Minute,
		Retention:    time.Duration(cfg.Agents.RetentionDays) * 24 * time.Hour,
		KeepRuns:     cfg.Agents.RetentionDays == 0,
	}
}

// itemArgs checks the item id before any state folder opens. The engine checks it again.
func itemArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return errors.New("give the item first: an item id such as 12 or fix-login")
	}

	return itemID(args[0])
}

// itemID refuses an item id that the grammar of an item does not accept.
func itemID(id string) error {
	if model.ItemKind(id) == "" {
		return fmt.Errorf("%w %q: use an item id such as 12 or fix-login", flow.ErrBadItem, id)
	}

	return nil
}

// oneItem accepts exactly one valid item id.
func oneItem(cmd *cobra.Command, args []string) error {
	return errors.Join(cobra.ExactArgs(1)(cmd, args), itemArgs(cmd, args))
}

func addConfigFlag(command *cobra.Command, target *string) {
	command.Flags().StringVar(target, configFlag, "", "the config folder (default: $"+config.ConfigDirVariable+", then ~/.config/beehave)")
}

const runLong = "Take one item through its workflow.\nName the project when the config holds more than one.\n\n" +
	"The steps of the workflow file run in file order. Name the file with --workflow, or keep " + defaultWorkflowFile + " in the current folder. " +
	"beehave checks the file first and refuses on any problem. If no file is named and the folder has none, beehave run refuses, unless the item has an open run.\n\n" +
	"If an earlier run of the item is open, beehave run continues it with the file that the run pinned. Press Ctrl-C to stop the command: the run stays open, and beehave run continues it.\n" +
	"If the last run failed, beehave run refuses and names the step that failed. Run beehave resume, then beehave run.\n" +
	"After a finished or a canceled run, beehave run starts a new run.\n\n" +
	"If another engine holds the state folder, beehave run gives the item to that engine and waits for the result. " +
	"The engine must be the same build, or the command refuses. The run waits in the items queue, so agents.max_concurrent applies, and its progress shows in the log of that engine. " +
	"If that engine stops, the run stays queued and the command exits with code 1: start the engine or run beehave run again.\n\n" +
	"The run starts whatever the usage caps say, and a run with no agent step never reads them. The first agent step that names no runner picks one under the caps. " +
	"If none is under the caps, the step waits for the earliest reset time that is known, then picks again. If no reset time is known, the step fails and says why. " +
	"With --" + overCapFlag + ", the step takes the runner with the lowest use instead of waiting, and the run records that the runner was over its cap."

func (f flowCommands) runCommand() *cobra.Command {
	var (
		flag, workflowFile string
		overCap            bool
	)

	command := &cobra.Command{
		Use:   "run <item> [project]",
		Short: "Take one item through its workflow",
		Long:  runLong,
		Args:  runArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return f.run(cmd, args, flag, workflowFile, overCap) },
	}
	addConfigFlag(command, &flag)
	command.Flags().BoolVar(&overCap, overCapFlag, false, "let the first agent step take a runner that is over its usage cap, in place of waiting for a reset")
	command.Flags().StringVar(&workflowFile, workflowFlag, "", "the workflow file (default: "+defaultWorkflowFile+" in the current folder, when it exists)")

	return command
}

// run validates the config, the project and the workflow file, then takes the item to the end of its run.
func (f flowCommands) run(cmd *cobra.Command, args []string, flag, workflowFile string, overCap bool) error {
	cfg, opts, err := f.options(cmd.ErrOrStderr(), flag)
	if err != nil {
		return err
	}

	project, err := f.target(cfg, args)
	if err != nil {
		return err
	}

	file, err := f.checkedWorkflow(cmd, workflowFile, cfg.Projects[project])
	if err != nil && !errors.Is(err, errNoWorkflow) {
		return err
	}

	// A folder without a workflow file may still hold an open run, which continues with the file that it pinned. runItem decides.
	opts.OverCap = overCap

	return f.runItem(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg, opts, args[0], project, file)
}

// refuseNoRepositoryCalls refuses the file before any step runs when the project has no repository and the file calls a builtin that needs one.
// A run with no file continues a pinned run, which passed this check when it started.
func refuseNoRepositoryCalls(stderr io.Writer, project config.Project, file string) error {
	if file == "" || project.HasRepository() {
		return nil
	}

	calls := workflow.CheckNoRepository(file, project.Name)
	if len(calls) == 0 {
		return nil
	}

	printProblems(stderr, file, calls)

	return &ExitError{Code: exitWorkflowProblems}
}

// target returns the project that run names, after it checks that the project knows the item.
func (f flowCommands) target(cfg config.Config, args []string) (string, error) {
	project, err := chooseProject(cfg, args[1:])
	if err != nil {
		return "", err
	}

	return project, f.knownItem(cfg.Projects[project].Project, args[0])
}

// checkedWorkflow returns the workflow file that run uses after it passes the check, and the check of the calls that the project cannot make.
func (f flowCommands) checkedWorkflow(cmd *cobra.Command, named string, project config.Project) (string, error) {
	file, err := chooseWorkflow(named)
	if err != nil {
		return "", err
	}

	if err := runCheckWorkflow(cmd.ErrOrStderr(), file); err != nil {
		return file, err
	}

	return file, refuseNoRepositoryCalls(cmd.ErrOrStderr(), project, file)
}

// missingWorkflow is the refusal of a run that has no workflow file to start with.
func missingWorkflow() error {
	return fmt.Errorf("%w: %s is not in the current folder. Name a workflow file with --%s", errNoWorkflow, defaultWorkflowFile, workflowFlag)
}

// chooseWorkflow returns the workflow file that run uses.
// A file named with --workflow must exist. Without the flag, workflow.star in the current folder is used, and a folder without it is refused.
func chooseWorkflow(named string) (string, error) {
	if named != "" {
		if _, err := os.Stat(named); err != nil {
			return "", fmt.Errorf("read the workflow file: %w. Name a file that exists", err)
		}

		return named, nil
	}

	_, err := os.Stat(defaultWorkflowFile)

	switch {
	case err == nil:
		return defaultWorkflowFile, nil
	case errors.Is(err, os.ErrNotExist):
		return "", missingWorkflow()
	}

	return "", fmt.Errorf("read the workflow file %s: %w. Fix the access to the file, or name another file with --%s", defaultWorkflowFile, err, workflowFlag)
}

// runArgs accepts a valid item id and at most one project name.
func runArgs(cmd *cobra.Command, args []string) error {
	return errors.Join(cobra.RangeArgs(1, maxRunArgs)(cmd, args), itemArgs(cmd, args))
}

// chooseProject returns the project that run names. It may be omitted only when the config holds exactly one project.
func chooseProject(cfg config.Config, named []string) (string, error) {
	names := slices.Sorted(maps.Keys(cfg.Projects))

	if len(named) == 1 {
		if _, ok := cfg.Projects[named[0]]; !ok {
			return "", unknownProject(named[0], cfg)
		}

		return named[0], nil
	}

	if len(names) == 1 {
		return names[0], nil
	}

	if len(names) == 0 {
		return "", errors.New("the config holds no project: add a file to the projects folder")
	}

	return "", fmt.Errorf("the config holds %d projects: name one after the item: %s", len(names), strings.Join(names, ", "))
}

// runItem takes the item to the end of its run. An open run continues with the file that it pinned, so a folder without a workflow file is enough for it.
// The pinned file is read through the client, which launches nothing: a refused run never opens the engine.
// When an engine already holds the state folder, as beehave serve does, the run joins that engine and the command waits for the result.
func (f flowCommands) runItem(stdout, stderr io.Writer, cfg config.Config, opts flow.Options, item, project, file string) error {
	client, err := f.factory.Client(f.ctx, cfg, opts)
	if err != nil {
		return err
	}

	chosen, running, err := f.chooseRunFile(stdout, client, item, file)
	if err != nil {
		return errors.Join(err, client.Close())
	}

	if running {
		return f.joinEngine(stdout, stderr, client, item, project, chosen)
	}

	if err := client.Close(); err != nil {
		return err
	}

	return f.ownEngine(stdout, stderr, cfg, opts, item, project, chosen)
}

// ownEngine runs the item on an engine of this command, and joins the other engine when one takes the state folder first.
func (f flowCommands) ownEngine(stdout, stderr io.Writer, cfg config.Config, opts flow.Options, item, project, file string) error {
	// The engine writes the start of the run and the start and end of each step to stderr, so that stdout keeps the result lines.
	opts.Progress = stderr

	err := f.runOnEngine(stdout, cfg, opts, item, project, file)
	if !errors.Is(err, flow.ErrStateBusy) {
		return err
	}

	// An engine took the state folder after the check and before this command opened its own: join that one.
	return f.joinLater(stdout, stderr, cfg, opts, item, project, file)
}

// chooseRunFile returns the workflow file of the run, and whether an engine holds the state folder.
func (f flowCommands) chooseRunFile(stdout io.Writer, client Steering, item, file string) (string, bool, error) {
	pinned, err := f.openRunFile(stdout, client, item)
	if err != nil {
		return "", false, err
	}

	if file == "" && pinned == "" {
		return "", false, missingWorkflow()
	}

	running, err := client.EngineRuns()

	return cmp.Or(file, pinned), running, err
}

// joinLater opens a new client and joins the engine that holds the state folder.
func (f flowCommands) joinLater(stdout, stderr io.Writer, cfg config.Config, opts flow.Options, item, project, file string) error {
	client, err := f.factory.Client(f.ctx, cfg, opts)
	if err != nil {
		return err
	}

	return f.joinEngine(stdout, stderr, client, item, project, file)
}

// joinEngine gives the item to the engine that holds the state folder, waits for the result, and closes the client.
// The run sits in the queue of that engine, so agents.max_concurrent applies to it, and its progress lines go to the log of that engine.
func (f flowCommands) joinEngine(stdout, stderr io.Writer, client Steering, item, project, file string) error {
	// A failed write to stderr has no recovery.
	_, _ = fmt.Fprintf(stderr, "item %s joins the engine that runs: its progress shows in the log of that engine\n", item)

	result, err := client.JoinRun(f.ctx, item, project, file)
	if err = errors.Join(err, client.Close()); err != nil {
		if f.ctx.Err() != nil {
			return errInterrupted
		}

		return err
	}

	return finish(stdout, item, result)
}

// runOnEngine opens the engine, runs the item on it and shuts it down.
func (f flowCommands) runOnEngine(stdout io.Writer, cfg config.Config, opts flow.Options, item, project, file string) error {
	engine, err := f.factory.Engine(f.ctx, cfg, opts)
	if err != nil {
		return err
	}

	if err := engine.Launch(); err != nil {
		return errors.Join(err, engine.Shutdown())
	}

	return errors.Join(f.continueOrStart(stdout, engine, item, project, file), engine.Shutdown())
}

func (f flowCommands) continueOrStart(stdout io.Writer, engine Runner, item, project, file string) error {
	result, err := f.await(engine, item, project, file)
	if err != nil {
		return err
	}

	return finish(stdout, item, result)
}

// finish prints how the run ended. A stop ends the run without done, so it is a failed run for a script.
func finish(stdout io.Writer, item string, result flow.ItemResult) error {
	printResult(stdout, item, result)

	if result.Stopped != nil {
		return &ExitError{Code: exitError}
	}

	return nil
}

// openRunFile returns the file that the open run of the item pinned, or an empty name. It says which file the run continues with.
// beehave run ignores --workflow when it joins an open run.
func (f flowCommands) openRunFile(stdout io.Writer, client Steering, item string) (string, error) {
	pinned, err := client.PinnedFile(item)
	if err != nil || pinned == "" {
		return "", err
	}

	// A failed write to stdout has no recovery.
	_, _ = fmt.Fprintf(stdout, "item %s continues its open run with its pinned file %s\n", item, pinned)

	return pinned, nil
}

// endNote says why a run ended before the merge: a send back or a close. It is empty for any other end.
func endNote(result flow.ItemResult) string {
	switch {
	case result.SentBack != "":
		return "went back to draft: " + model.OneLine(result.SentBack)
	case result.Closed != "":
		return "ended: " + model.OneLine(result.Closed)
	}

	return ""
}

// printResult says how a finished run ended: finished, or back at draft.
func printResult(stdout io.Writer, item string, result flow.ItemResult) {
	// A failed write to stdout has no recovery.
	if note := endNote(result); note != "" {
		_, _ = fmt.Fprintf(stdout, "item %s %s\n", item, note)

		return
	}

	if stop := result.Stopped; stop != nil {
		_, _ = fmt.Fprintf(stdout, "item %s stopped at step %s with exit code %d\n", item, stop.Step, stop.RC)

		if stop.Output != "" {
			_, _ = fmt.Fprintf(stdout, "%s\n", strings.TrimSuffix(stop.Output, "\n"))
		}

		return
	}

	_, _ = fmt.Fprintf(stdout, "item %s finished\n", item)
}

// await runs the item and waits for its result. An interrupt of the command ends the wait.
func (f flowCommands) await(engine Runner, item, project, file string) (flow.ItemResult, error) {
	type outcome struct {
		result flow.ItemResult
		err    error
	}

	done := make(chan outcome, 1)

	go func() {
		result, err := engine.RunFile(item, project, file)
		done <- outcome{result: result, err: err}
	}()

	select {
	case finished := <-done:
		return finished.result, finished.err
	case <-f.ctx.Done():
		return flow.ItemResult{}, errInterrupted
	}
}

func (f flowCommands) statusCommand() *cobra.Command {
	var (
		flag   string
		item   string
		asJSON bool
		days   int
	)

	command := &cobra.Command{
		Use:   "status [run id]",
		Short: "List the runs, or the steps of one run",
		Long:  statusHelp,
		Args:  cobra.MaximumNArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return f.status(cmd, flag, item, args, asJSON, days) },
	}
	addConfigFlag(command, &flag)
	command.Flags().IntVar(&days, daysFlag, defaultDays, "show the per-day figures of this many local days, the current day included (the list only)")
	command.Flags().StringVar(&item, itemFlag, "", "list the lanes of this item across all its runs")
	command.Flags().BoolVar(&asJSON, jsonFlag, false, "print the runs, the steps of one run, or the lanes of one item, as JSON for a script")

	return command
}

// jsonFlag is the name of the flag that makes the per-run view print JSON.
const jsonFlag = "json"

// itemFlag is the name of the flag that makes status list the lanes of one item.
const itemFlag = "item"

// daysFlag is the name of the flag that sets how many local days the per-day figures cover. defaultDays is its value when the flag is absent.
const (
	daysFlag    = "days"
	defaultDays = 7
)

// errDaysBelowOne is returned when --days is below 1: a window needs at least the current day.
var errDaysBelowOne = errors.New("--" + daysFlag + " takes a number of days of 1 or more")

const statusHelp = "List the runs, the questions that wait for you and the runs another version must finish. Each line starts with the run id.\n\n" +
	"A line also shows the item and the project of the run, the cost so far against the run cap, the step that runs now, the last gate result with its head, and the fix and review rounds used against their caps. " +
	"A run that waits shows its question, the people it asks, how many of the quorum answered and who, the choices, the next reminder, and the deadline with the time left. " +
	"A run that stopped or was sent back shows the fields of its result.\n\n" +
	"After the runs, the list shows each schedule with its last tick and whether an engine fires it (it shows no next due time), then the newest review poll and why the poll is not scheduled.\n\n" +
	"Add --" + jsonFlag + " to the list for one JSON object with the same data: runs, schedules, days, poll and poll_not_scheduled. " +
	"Each run has the fields id, state, item, project, question, asked, deadline, time_left_seconds, merge, sent_back, closed and stopped. " +
	"A field with no value is left out. The list reads recorded state only.\n\n" +
	"The list also shows the figures of each local day from the last --" + daysFlag + " days (default 7, the current day included): the runs that started, the runs that ended as done, failed or canceled, and the spend of those runs with the estimate and unknown marks of the run total. " +
	"The day is the local day of the clock of this command. A run that spans midnight counts as started on its start day and as ended on its end day. " +
	"The spend of a run counts once, on the day it ended, or on the day it started while it is open. A day with no run is left out, and a window with no run shows no section. " +
	"The JSON list has the same figures in days, one object for each day.\n\n" +
	"Give a run id to list the steps of that run, one line each: the step name, the start time, the duration, the outcome and the fields that decide the flow. " +
	"A step that runs now shows as running with its elapsed time. Add --" + jsonFlag + " for one JSON object that a script reads.\n\n" +
	"Give --" + itemFlag + " to list the lanes of one item across all its runs, one line each: the run id, the step name, the kind of the lane " +
	"(fix, rework, relay, review or design), the outcome and the fields that decide the flow."

// errItemAndRun is returned when status gets both a run id and --item: the two views answer different questions.
var errItemAndRun = errors.New("give a run id or --" + itemFlag + ", not both")

// status prints the runs of the store, or the steps of the run that the command names.
func (f flowCommands) status(cmd *cobra.Command, flag, item string, args []string, asJSON bool, days int) error {
	out := cmd.OutOrStdout()

	switch {
	case item != "" && len(args) == 1:
		return errItemAndRun
	case item != "":
		return f.withClient(cmd, flag, func(client Steering) error { return printLanes(out, client, item, asJSON) })
	case len(args) == 1:
		return f.withClient(cmd, flag, func(client Steering) error { return printSteps(out, client, args[0], asJSON) })
	}

	if days < 1 {
		return errDaysBelowOne
	}

	return f.withClient(cmd, flag, func(client Steering) error { return printRuns(out, client, f.clock(), days, asJSON) })
}

// clock returns the clock of the status view.
func (f flowCommands) clock() func() time.Time {
	if f.factory.Now == nil {
		return time.Now
	}

	return f.factory.Now
}

// printRuns writes the runs of the store, the schedules, the per-day figures, then the review poll section. With asJSON it writes the same data as one JSON object.
func printRuns(out io.Writer, client Steering, clock func() time.Time, days int, asJSON bool) error {
	now := clock()

	list, err := readStatusList(client, flow.DaysSince(now, days))
	if err != nil {
		return err
	}

	if asJSON {
		return printListJSON(out, list, now)
	}

	printStatus(out, list.Runs, now)
	printEngine(out, list.Engine)
	printSchedules(out, list.Schedules)
	printDays(out, list.Days)
	printPollSection(out, list)

	return nil
}

// printEngine says what holds the state folder: the serve engine with its pid and start time, the engine of a run with its pid, or no engine.
func printEngine(stdout io.Writer, engine *flow.EngineInfo) {
	// A failed write to stdout has no recovery.
	switch {
	case engine == nil:
		_, _ = fmt.Fprintln(stdout, "\nno engine runs")
	case engine.Mode == flow.ModeServe:
		_, _ = fmt.Fprintf(stdout, "\nengine: serve, pid %d, since %s\n", engine.PID, engine.Since.Format(time.RFC3339))
	case engine.Mode == flow.ModeRun:
		_, _ = fmt.Fprintf(stdout, "\nengine: run, pid %d\n", engine.PID)
	default:
		_, _ = fmt.Fprintln(stdout, "\nengine: running")
	}
}

// printDays writes the per-day figures, one line for each day. It writes nothing when the window has no run.
func printDays(stdout io.Writer, days []flow.DayFigures) {
	if len(days) == 0 {
		return
	}

	// A failed write to stdout has no recovery.
	_, _ = fmt.Fprintln(stdout, "\nper day")

	for _, day := range days {
		_, _ = fmt.Fprintln(stdout, day.Text())
	}
}

// printSchedules writes one line for each schedule: its name, its text, its last tick and whether it fires. It writes nothing when no schedule exists.
func printSchedules(stdout io.Writer, schedules []flow.ScheduleInfo) {
	if len(schedules) == 0 {
		return
	}

	// A failed write to stdout has no recovery.
	_, _ = fmt.Fprintln(stdout, "\nschedules")

	for _, schedule := range schedules {
		_, _ = fmt.Fprintln(stdout, scheduleLine(schedule))
	}
}

// scheduleLine describes one schedule: its name, its text, its last tick, then paused, firing or not firing.
func scheduleLine(schedule flow.ScheduleInfo) string {
	last := "no tick yet"
	if schedule.LastTick != nil {
		last = "last tick " + schedule.LastTick.Format(time.RFC3339)
	}

	state := "not firing, no engine runs"

	switch {
	case schedule.Paused:
		state = "paused"
	case schedule.Firing:
		state = "firing"
	}

	return schedule.Name + "\t" + schedule.Schedule + "\t" + last + "\t" + state
}

// printPollSection writes the newest review poll, then the cause when the last launch could not schedule the poll.
func printPollSection(stdout io.Writer, list statusList) {
	if list.Poll != nil {
		printPoll(stdout, *list.Poll)
	}

	if list.PollNotScheduled != "" {
		// A failed write to stdout has no recovery.
		_, _ = fmt.Fprintf(stdout, "\nthe review poll is not scheduled: %s\n", model.OneLine(list.PollNotScheduled))
	}
}

// printPoll writes the section of the newest review poll: its time, then the rows of each project.
// A project that the poll could not read shows the cause, never an empty list.
func printPoll(stdout io.Writer, poll flow.PollOutput) {
	if poll.Failure != "" {
		// A failed write to stdout has no recovery.
		_, _ = fmt.Fprintf(stdout, "\nthe review poll failed at %s: %s\n", poll.At.Format(time.RFC3339), model.OneLine(poll.Failure))

		return
	}

	// A failed write to stdout has no recovery.
	_, _ = fmt.Fprintf(stdout, "\nreview poll at %s\n", poll.At.Format(time.RFC3339))

	for _, project := range poll.Projects {
		printPollProject(stdout, project)
	}
}

// printPollProject writes the lines of one project: its rows, or the reason it has none.
func printPollProject(stdout io.Writer, project flow.PollProject) {
	// A failed write to stdout has no recovery.
	switch {
	case project.NoForge:
		_, _ = fmt.Fprintf(stdout, "%s: no forge, not polled\n", project.Project)
	case project.Unread != "":
		_, _ = fmt.Fprintf(stdout, "%s\t%s\n", project.Project, model.OneLine(project.Unread))
	case len(project.Rows) == 0:
		_, _ = fmt.Fprintf(stdout, "%s\tnothing to review\n", project.Project)
	}

	for _, row := range project.Rows {
		_, _ = fmt.Fprintf(stdout, "%s\t%s\n", project.Project, row)
	}
}

// printStatus writes one line for each run. A paused run shows its question; a stranded run names the version that finishes it.
func printStatus(stdout io.Writer, infos []flow.RunInfo, now time.Time) {
	if len(infos) == 0 {
		// A failed write to stdout has no recovery.
		_, _ = fmt.Fprintln(stdout, "no runs")

		return
	}

	for _, info := range infos {
		_, _ = fmt.Fprintln(stdout, statusLine(info, now))
	}
}

// statusLine describes one run: its id and state, the one thing that the run waits for or ended with, then its item and project.
func statusLine(info flow.RunInfo, now time.Time) string {
	line := fmt.Sprintf("%s\t%s", info.ID, info.State)

	switch {
	case info.OlderSteps:
		line += "\tstranded: made by an older step format. Start the beehave build that made it to finish it"
	case info.Stranded:
		line += fmt.Sprintf("\tstranded: start beehave version %s to finish it", info.Version)
	case info.Question != "":
		line += "\tpaused: " + model.OneLine(info.Question) + askedTail(info.Asked)
	default:
		line += finishedNote(info)
	}

	line += notesTail(info, now)

	if len(info.Others) > 0 {
		line += "\tother orchestrators: " + model.OneLine(strings.Join(info.Others, "; "))
	}

	return line + itemTail(info)
}

// itemTail is the tab and text that names the item of a run and its project. It is empty when the stored input does not read.
func itemTail(info flow.RunInfo) string {
	switch {
	case info.Item == "":
		return ""
	case info.Project == "":
		return "\titem " + info.Item
	}

	return "\titem " + info.Item + " of " + info.Project
}

// peopleTail says who a wait asks, how many of the quorum answered and who.
func peopleTail(asked *flow.Asked) string {
	text := fmt.Sprintf("asks %s, %d of %d answered", strings.Join(asked.From, ", "), len(asked.Heard), asked.Quorum)
	if len(asked.Heard) > 0 {
		text += " (" + strings.Join(asked.Heard, ", ") + ")"
	}

	return text
}

// askedTail describes the people and the choices of a wait: who is asked, how many must answer, who did, and the texts an answer may be.
// It is empty for a wait that names neither.
func askedTail(asked *flow.Asked) string {
	if asked == nil {
		return ""
	}

	var parts []string

	if len(asked.From) > 0 {
		parts = append(parts, peopleTail(asked))
	}

	if len(asked.Choices) > 0 {
		parts = append(parts, "choices "+strings.Join(asked.Choices, ", "))
	}

	if !asked.NextRemind.IsZero() {
		parts = append(parts, fmt.Sprintf("next reminder %s (%d of %d sent)", asked.NextRemind.UTC().Format(time.RFC3339), asked.Reminded, asked.Reminders))
	}

	return "\t" + model.OneLine(strings.Join(parts, "; "))
}

// notesTail is the tab-separated progress notes of a run: its cost, step, wait deadline, gate and rounds. It is empty for a run with none.
func notesTail(info flow.RunInfo, now time.Time) string {
	notes := info.NotesAt(now)
	if len(notes) == 0 {
		return ""
	}

	return "\t" + strings.Join(notes, "\t")
}

// finishedNote is the tab and text that a run adds to its status line when it ended: a send back, a close, a stop or a merge state. It is empty for none.
func finishedNote(info flow.RunInfo) string {
	switch {
	case info.SentBack != "":
		return "\tsent back to draft: " + model.OneLine(info.SentBack)
	case info.Closed != "":
		return "\tended: " + model.OneLine(info.Closed)
	case info.Stopped != nil:
		return stoppedNote(info.Stopped)
	case info.Merge != "":
		return "\tmerge: " + info.Merge
	}

	return ""
}

// stoppedOutputLimit is the most characters of the output of a stopping step that a status line shows.
const stoppedOutputLimit = 120

// stoppedNote is the text for a run that a command ended: the step, the exit code and the start of what the step printed.
// The output is the result of the stop, so the owner reads the cause without opening the step view.
func stoppedNote(stopped *flow.Stopped) string {
	note := fmt.Sprintf("\tstopped at %s rc %d", stopped.Step, stopped.RC)

	text := model.Clip(stopped.Output, stoppedOutputLimit, "…")
	if text == "" {
		return note
	}

	return note + ": " + text
}

// withClient opens the client over the validated config, runs fn and closes the client.
func (f flowCommands) withClient(cmd *cobra.Command, flag string, fn func(Steering) error) error {
	cfg, opts, err := f.options(cmd.ErrOrStderr(), flag)
	if err != nil {
		return err
	}

	client, err := f.factory.Client(f.ctx, cfg, opts)
	if err != nil {
		return err
	}

	return errors.Join(fn(client), client.Close())
}

// sendAnswer sends the text as a plain answer, or as the answer of the person that --as names.
func sendAnswer(client Steering, item, as, text string) error {
	if as == "" {
		return client.Answer(item, text)
	}

	return client.AnswerAs(item, as, text)
}

// manyAnswer holds the flags that make beehave answer reply to many waiting runs.
type manyAnswer struct {
	all             bool
	project, asking string
	items           []string
}

// given returns the selector flags that the owner typed, even with an empty value.
func given(cmd *cobra.Command) []string {
	var names []string

	for _, name := range []string{"project", "question", "items"} {
		if cmd.Flags().Changed(name) {
			names = append(names, name)
		}
	}

	return names
}

// emptySelector returns an error for a selector flag that the owner typed with an empty value.
func (m manyAnswer) emptySelector(names []string) error {
	for _, name := range names {
		if (name == "project" && m.project == "") || (name == "question" && m.asking == "") ||
			(name == "items" && len(m.items) == 0) {
			return fmt.Errorf("%w: --%s is empty, and an empty selector would pick every run. Give a value or remove the flag", errAnswerShape, name)
		}
	}

	return nil
}

// picks reports whether the run waits for an answer and fits every selector that is set.
func (m manyAnswer) picks(info flow.RunInfo) bool {
	return info.Waiting() && info.Item != "" &&
		(m.project == "" || info.Project == m.project) &&
		strings.Contains(info.Question, m.asking) &&
		(len(m.items) == 0 || slices.Contains(m.items, info.Item))
}

// errNoWaiting is returned when beehave answer --all finds no waiting run that fits the selectors.
var errNoWaiting = errors.New("no waiting run matches. Run beehave status to see the runs that wait for an answer")

// errNotTaken is returned when at least one run of beehave answer --all refused the answer.
var errNotTaken = errors.New("some runs did not take the answer")

// errAnswerShape is returned when the item and the --all flag do not fit together.
var errAnswerShape = errors.New("give an item and the text, or --all with the text and no item")

// answerArgs checks the arguments of beehave answer: an item and the text, or with --all the text alone.
func (m *manyAnswer) answerArgs(cmd *cobra.Command, args []string) error {
	names := given(cmd)

	switch {
	case m.all && len(args) == 0:
		return fmt.Errorf("%w: the text is missing", errAnswerShape)
	case m.all && len(args) > 1 && itemID(args[0]) == nil:
		return fmt.Errorf("%w: %q looks like an item, and --all sends the text to every run. Use --items %s to pick the item, or quote the text as one argument", errAnswerShape, args[0], args[0])
	case m.all:
		return errors.Join(m.emptySelector(names), m.checkItems())
	case len(names) > 0:
		return fmt.Errorf("%w: a selector needs --all", errAnswerShape)
	}

	return itemArgs(nil, args)
}

// checkItems refuses an entry of the --items list that is not an item id.
func (m manyAnswer) checkItems() error {
	for _, item := range m.items {
		if err := itemID(item); err != nil {
			return err
		}
	}

	return nil
}

// answerMany answers every waiting run that fits the selectors, one run at a time through the answer of a single run.
// It prints one line for each run and goes on after a refusal. The error says how many runs refused.
func answerMany(out io.Writer, client Steering, m manyAnswer, as, text string) error {
	infos, err := client.Status()
	if err != nil {
		return err
	}

	items := m.picked(infos)
	if len(items) == 0 {
		return errNoWaiting
	}

	if refused := replyAll(out, client, items, as, text); refused > 0 {
		return fmt.Errorf("%w: %d of %d runs did not take the answer", errNotTaken, refused, len(items))
	}

	return nil
}

// replyAll answers each item in turn and returns how many refused.
func replyAll(out io.Writer, client Steering, items []string, as, text string) int {
	refused := 0

	for _, item := range items {
		if !replyTo(out, client, item, as, text) {
			refused++
		}
	}

	return refused
}

// replyTo answers one item, prints its line, and reports whether the run took the answer.
func replyTo(out io.Writer, client Steering, item, as, text string) bool {
	err := sendAnswer(client, item, as, text)
	if err != nil {
		_, _ = fmt.Fprintf(out, "%s\trefused: %s\n", item, model.OneLine(err.Error()))

		return false
	}

	_, _ = fmt.Fprintf(out, "%s\tanswered\n", item)

	return true
}

// picked returns the item of each run that the selectors pick, once each, in the order of the status list.
func (m manyAnswer) picked(infos []flow.RunInfo) []string {
	var items []string

	for _, info := range infos {
		if m.picks(info) && !slices.Contains(items, info.Item) {
			items = append(items, info.Item)
		}
	}

	return items
}

// addFlags adds the flags of beehave answer --all.
func (m *manyAnswer) addFlags(command *cobra.Command) {
	command.Flags().BoolVar(&m.all, "all", false, "answer every paused run that fits the selectors; give the text and no item")
	command.Flags().StringVar(&m.project, "project", "", "with --all: answer only runs of this project")
	command.Flags().StringVar(&m.asking, "question", "", "with --all: answer only runs whose question holds this text")
	command.Flags().StringSliceVar(&m.items, "items", nil, "with --all: answer only these items, a comma-separated list")
}

// answer runs beehave answer: one item, or with --all every run that the selectors pick.
func (f flowCommands) answer(cmd *cobra.Command, flag string, many manyAnswer, as string, args []string) error {
	if many.all {
		return f.withClient(cmd, flag, func(client Steering) error {
			return answerMany(cmd.OutOrStdout(), client, many, as, strings.Join(args, " "))
		})
	}

	return f.withClient(cmd, flag, func(client Steering) error { return sendAnswer(client, args[0], as, strings.Join(args[1:], " ")) })
}

const answerLong = "Answer the question of a paused item.\n" +
	"A wait that names people takes an answer only from them. Add --as <name> to say who answers.\n" +
	"A wait that sets choices takes only those texts. beehave status shows the people, the choices and who answered.\n" +
	"beehave does not prove the name: anyone who can run beehave answer on the run can give any name that the wait asks.\n\n" +
	"Add --all to give the same answer to every run that beehave status shows as paused with a question, and give the text alone, with no item. " +
	"Narrow the runs with --project (the project of the run), --question (a text that the question holds) and --items (a list of item ids). A run must fit every selector that is set.\n" +
	"Each run takes the answer as beehave answer <item> would send it, and keeps every refusal rule of that command: a run that refuses prints one line with the reason, and the other runs still take the answer. " +
	"The command prints one line for each run. It exits 0 only when every run took the answer, and exits 1 when a run refused or when no run waits."

func (f flowCommands) answerCommand() *cobra.Command {
	var flag, as string

	var many manyAnswer

	command := &cobra.Command{
		Use:   "answer <item> <text> | answer --all [selectors] <text>",
		Short: "Answer the question of a paused item, or of many",
		Long:  answerLong,
		Args:  many.answerArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return f.answer(cmd, flag, many, as, args) },
	}
	addConfigFlag(command, &flag)
	command.Flags().StringVar(&as, "as", "", "name of the person who answers a wait that names people")
	many.addFlags(command)

	return command
}

const resumeLong = "Reopen the cancelled or failed run of an item. The run restarts from its last finished step.\n" + //nolint:misspell // user-facing state word, same as the cancelled run state
	"Resume takes a fresh claim on the item first, and refuses if another holder has it.\n" +
	"A canceled run that left its claim gives it back at the next beehave run.\n" +
	"Run beehave run for the item next: it continues the reopened run and renews the claim.\n" +
	"A run that another build of beehave started is refused. Add --on-this-build to move it to this build, from its last finished step.\n" +
	"The move is refused when the workflow file or a file the run pinned changed since the run started. Scripts outside the repository are not pinned."

// resumeItem reopens the run of an item, and moves it to this build first when the owner asked for that.
func resumeItem(client Steering, item string, onThisBuild bool) error {
	if onThisBuild {
		return client.ResumeOnThisBuild(item)
	}

	return client.Resume(item)
}

func (f flowCommands) resumeCommand() *cobra.Command {
	var flag string

	var onThisBuild bool

	command := &cobra.Command{
		Use:   "resume <item>",
		Short: "Reopen the cancelled or failed run of an item", //nolint:misspell // user-facing state word, same as the cancelled run state
		Long:  resumeLong,
		Args:  oneItem,
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withClient(cmd, flag, func(client Steering) error { return resumeItem(client, args[0], onThisBuild) })
		},
	}
	addConfigFlag(command, &flag)
	command.Flags().BoolVar(&onThisBuild, "on-this-build", false, "move a run that another build started to this build")

	return command
}

func (f flowCommands) cancelCommand() *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "cancel <item>",
		Short: "Stop the open run of an item",
		Args:  oneItem,
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withClient(cmd, flag, func(client Steering) error { return client.Cancel(args[0]) })
		},
	}
	addConfigFlag(command, &flag)

	return command
}
