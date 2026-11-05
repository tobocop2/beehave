package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
)

// exitSecondSignal is the exit code of beehave serve when a second signal ends a slow shutdown: 128 plus SIGINT.
const exitSecondSignal = 130

// errServeBusy is the refusal of beehave serve when another engine holds the state folder.
var errServeBusy = errors.New("another beehave engine owns this state folder; see beehave status")

const serveLong = "Keep an engine up on the state folder, in the foreground.\n\n" +
	"beehave serve resumes the open runs of this build, applies the schedules (the review poll and the project schedules) and waits. " +
	"A beehave run on the same state folder gives its item to this engine and prints the result. The agents.max_concurrent limit applies to all of them. " +
	"The review poll is off while agents.review_poll_minutes is 0: under beehave serve, set 60.\n\n" +
	"SIGINT and SIGTERM stop the engine. The open runs stay open, and the next engine resumes them. A second signal during the stop exits at once with code 130. " +
	"A second engine on the same state folder is refused. beehave serve starts no daemon and writes no pid file: run it from launchd or systemd, as docs/serve.md shows."

func (f flowCommands) serveCommand() *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "serve",
		Short: "Keep an engine up on the state folder",
		Long:  serveLong,
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return f.serve(cmd, flag) },
	}
	addConfigFlag(command, &flag)

	return command
}

// serve opens the engine, launches it and waits for a signal, then shuts the engine down.
// The command takes the signals itself: the first one stops the engine, and a second one ends a stop that takes too long.
func (f flowCommands) serve(cmd *cobra.Command, flag string) error {
	signals := make(chan os.Signal, 2) //nolint:mnd // one for the stop and one for the second signal that ends it
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	defer signal.Stop(signals)

	engine, err := f.launchServing(cmd, flag)
	if err != nil {
		return err
	}

	// A failed write to stderr has no recovery.
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "beehave serve: the engine runs; stop it with SIGTERM or Ctrl-C")

	<-signals

	return stopEngine(engine, signals)
}

// launchServing opens the engine in serve mode and launches it. A failed launch shuts the engine down.
func (f flowCommands) launchServing(cmd *cobra.Command, flag string) (Runner, error) { //nolint:ireturn // the engine factory returns the Runner interface
	cfg, opts, err := f.options(cmd.ErrOrStderr(), flag)
	if err != nil {
		return nil, err
	}

	opts.Mode = flow.ModeServe
	opts.Progress = cmd.ErrOrStderr()

	return f.startEngine(cfg, opts)
}

// startEngine opens the engine and launches it. A lock held by another engine gets the text for serve.
func (f flowCommands) startEngine(cfg config.Config, opts flow.Options) (Runner, error) { //nolint:ireturn // the engine factory returns the Runner interface
	engine, err := f.factory.Engine(f.ctx, cfg, opts)
	if errors.Is(err, flow.ErrStateBusy) {
		return nil, errServeBusy
	}

	if err != nil {
		return nil, err
	}

	if err := engine.Launch(); err != nil {
		return nil, errors.Join(err, engine.Shutdown())
	}

	return engine, nil
}

// stopEngine shuts the engine down once. A signal that arrives before the shutdown ends makes the command exit at once with code 130.
func stopEngine(engine Runner, signals <-chan os.Signal) error {
	done := make(chan error, 1)

	go func() { done <- engine.Shutdown() }()

	select {
	case err := <-done:
		return err
	case <-signals:
		return &ExitError{Code: exitSecondSignal}
	}
}
