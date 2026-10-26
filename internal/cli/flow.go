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

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
)

// maxRunArgs is the item and the optional project.
const maxRunArgs = 2

// ErrInterrupted is returned by run when the user stops the command before the item ends.
var ErrInterrupted = errors.New("stopped before the item finished: run obee run again to continue it")

// Runner is the engine that the run command drives. Call Launch, then Run, then Shutdown.
type Runner interface {
	Launch() error
	Run(item, project string) (flow.ItemResult, error)
	Shutdown() error
}

// Steering reads and steers runs without running any.
type Steering interface {
	Status() ([]flow.RunInfo, error)
	Answer(item, text string) error
	Resume(item string) error
	Cancel(item string) error
	Close() error
}

// Flow opens the engine and the client over the validated config. A nil factory leaves its commands out.
type Flow struct {
	Engine func(context.Context, config.Config, flow.Options) (Runner, error)
	Client func(context.Context, flow.Options) (Steering, error)
}

// flowCommands holds what the five commands share: the process environment, the build version and the factories.
type flowCommands struct {
	ctx     context.Context //nolint:containedctx // cobra gives a command no context of its own before it runs; the root passes it in
	env     map[string]string
	version string
	factory Flow
}

func (f flowCommands) add(root *cobra.Command) {
	if f.factory.Engine != nil {
		root.AddCommand(f.runCommand())
	}

	if f.factory.Client == nil {
		return
	}

	root.AddCommand(f.statusCommand(), f.answerCommand(), f.resumeCommand(), f.cancelCommand())
}

// options loads and validates the config folder, then builds the options of the engine and the client.
// Every command passes here, so each refuses the same unusable settings.
func (f flowCommands) options(stderr io.Writer, flag string) (config.Config, flow.Options, error) {
	dir, err := config.FolderPath(f.env, flag)
	if err != nil {
		return config.Config{}, flow.Options{}, err
	}

	cfg, problems := config.LoadFolder(dir, f.env)
	if len(problems) > 0 {
		for _, problem := range problems {
			// A failed write to stderr has no recovery.
			_, _ = fmt.Fprintln(stderr, problem)
		}

		return cfg, flow.Options{}, &ExitError{Code: exitProblems}
	}

	state, err := config.StateDir(f.env)
	if err != nil {
		return cfg, flow.Options{}, err
	}

	return cfg, flow.Options{StateDir: state, Version: f.version, MaxConcurrent: cfg.Agents.MaxConcurrent, Env: f.env}, nil
}

// itemArgs checks the item id before any state folder opens. The engine checks it again.
func itemArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return errors.New("give the item first: a forge number or a beads id")
	}

	if model.ItemKind(args[0]) == "" {
		return fmt.Errorf("%w %q: use a forge number or a beads id", flow.ErrBadItem, args[0])
	}

	return nil
}

// oneItem accepts exactly one valid item id.
func oneItem(cmd *cobra.Command, args []string) error {
	return errors.Join(cobra.ExactArgs(1)(cmd, args), itemArgs(cmd, args))
}

func addConfigFlag(command *cobra.Command, target *string) {
	command.Flags().StringVar(target, configFlag, "", "the config folder (default: $"+config.ConfigDirVariable+", then ~/.config/obee)")
}

func (f flowCommands) runCommand() *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "run <item> [project]",
		Short: "Take one item through plan, implement, review and fix rounds",
		Long: "Take one item through plan, implement, review and fix rounds.\nName the project when the config holds more than one.\n\n" +
			"If an earlier run of the item is open, obee run continues it. Press Ctrl-C to stop the command: the run stays open, and obee run continues it.\n" +
			"If the last run failed, obee run refuses and names the step that failed. Run obee resume, then obee run.\n" +
			"After a finished or a canceled run, obee run starts a new run.",
		Args: runArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return f.run(cmd, args, flag) },
	}
	addConfigFlag(command, &flag)

	return command
}

// run validates the config and the project, then takes the item to the end of its run.
func (f flowCommands) run(cmd *cobra.Command, args []string, flag string) error {
	cfg, opts, err := f.options(cmd.ErrOrStderr(), flag)
	if err != nil {
		return err
	}

	project, err := chooseProject(cfg, args[1:])
	if err != nil {
		return err
	}

	return f.runItem(cmd.OutOrStdout(), cfg, opts, args[0], project)
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
			return "", fmt.Errorf("no project %q in the config: use one of %s", named[0], strings.Join(names, ", "))
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

func (f flowCommands) runItem(stdout io.Writer, cfg config.Config, opts flow.Options, item, project string) error {
	engine, err := f.factory.Engine(f.ctx, cfg, opts)
	if err != nil {
		return err
	}

	if err := engine.Launch(); err != nil {
		return errors.Join(err, engine.Shutdown())
	}

	result, err := f.await(engine, item, project)
	if err == nil {
		// A failed write to stdout has no recovery.
		_, _ = fmt.Fprintf(stdout, "item %s finished after %d fix rounds\n", item, result.FixRounds)
	}

	return errors.Join(err, engine.Shutdown())
}

// await runs the item and waits for its result. An interrupt of the command ends the wait.
func (f flowCommands) await(engine Runner, item, project string) (flow.ItemResult, error) {
	type outcome struct {
		result flow.ItemResult
		err    error
	}

	done := make(chan outcome, 1)

	go func() {
		result, err := engine.Run(item, project)
		done <- outcome{result: result, err: err}
	}()

	select {
	case finished := <-done:
		return finished.result, finished.err
	case <-f.ctx.Done():
		return flow.ItemResult{}, ErrInterrupted
	}
}

func (f flowCommands) statusCommand() *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "status",
		Short: "List the runs, the questions that wait for you and the runs another version must finish",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return f.status(cmd, flag) },
	}
	addConfigFlag(command, &flag)

	return command
}

// status prints the runs of the store.
func (f flowCommands) status(cmd *cobra.Command, flag string) error {
	return f.withClient(cmd, flag, func(client Steering) error {
		infos, err := client.Status()
		if err != nil {
			return err
		}

		printStatus(cmd.OutOrStdout(), infos)

		return nil
	})
}

// printStatus writes one line for each run. A paused run shows its question; a stranded run names the version that finishes it.
func printStatus(stdout io.Writer, infos []flow.RunInfo) {
	if len(infos) == 0 {
		// A failed write to stdout has no recovery.
		_, _ = fmt.Fprintln(stdout, "no runs")

		return
	}

	for _, info := range infos {
		line := fmt.Sprintf("%s\t%s", info.ID, info.State)

		switch {
		case info.Stranded:
			line += fmt.Sprintf("\tstranded: start obee version %s to finish it", info.Version)
		case info.Question != "":
			line += "\tpaused: " + oneLine(info.Question)
		}

		_, _ = fmt.Fprintln(stdout, line)
	}
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// withClient opens the client over the validated config, runs fn and closes the client.
func (f flowCommands) withClient(cmd *cobra.Command, flag string, fn func(Steering) error) error {
	_, opts, err := f.options(cmd.ErrOrStderr(), flag)
	if err != nil {
		return err
	}

	client, err := f.factory.Client(f.ctx, opts)
	if err != nil {
		return err
	}

	return errors.Join(fn(client), client.Close())
}

func (f flowCommands) answerCommand() *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "answer <item> <text>",
		Short: "Answer the question of a paused item",
		Args:  itemArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withClient(cmd, flag, func(client Steering) error { return client.Answer(args[0], strings.Join(args[1:], " ")) })
		},
	}
	addConfigFlag(command, &flag)

	return command
}

func (f flowCommands) resumeCommand() *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "resume <item>",
		Short: "Reopen the cancelled or failed run of an item", //nolint:misspell // user-facing state word, same as flow.RunCancelled
		Long: "Reopen the cancelled or failed run of an item. The run restarts from its last finished step.\n" + //nolint:misspell // user-facing state word, same as flow.RunCancelled
			"Resume takes a fresh claim on the item first, and refuses if another holder has it.\n" +
			"A canceled run that left its claim gives it back at the next obee run.\n" +
			"Run obee run for the item next: it continues the reopened run and renews the claim.",
		Args: oneItem,
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withClient(cmd, flag, func(client Steering) error { return client.Resume(args[0]) })
		},
	}
	addConfigFlag(command, &flag)

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
