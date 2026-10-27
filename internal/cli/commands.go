package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/tobocop2/obee/internal/brief"
	checkpkg "github.com/tobocop2/obee/internal/check"
)

// input is what a command body gets: the arguments, the flags and the two streams.
type input struct {
	ctx    context.Context //nolint:containedctx // cobra gives a command no context of its own before it runs; the root passes it in
	env    map[string]string
	args   []string
	stdout io.Writer
	stderr io.Writer
}

// Argument counts of the commands.
const (
	twoArgs   = 2
	threeArgs = 3
)

// spec is one command: its words, its arguments and its body. A body returns an ExitError for a code of its own.
// A command with flags sets bind instead of run: bind binds the flags to the options of the command and returns the body.
type spec struct {
	use   string
	short string
	args  cobra.PositionalArgs
	run   func(input) error
	bind  func(*pflag.FlagSet) func(input) error
}

// commandsOf builds one cobra command for each spec. Every command shares one exit mapping: a refusal or a failed read exits 2.
func commandsOf(ctx context.Context, env map[string]string, specs []spec) []*cobra.Command {
	commands := make([]*cobra.Command, 0, len(specs))

	for _, s := range specs {
		commands = append(commands, commandOf(ctx, env, s))
	}

	return commands
}

func commandOf(ctx context.Context, env map[string]string, s spec) *cobra.Command {
	command := &cobra.Command{Use: s.use, Short: s.short, Args: s.args}

	run := s.run
	if s.bind != nil {
		run = s.bind(command.Flags())
	}

	command.RunE = func(cmd *cobra.Command, args []string) error {
		err := run(input{ctx: ctx, env: env, args: args, stdout: cmd.OutOrStdout(), stderr: cmd.ErrOrStderr()})

		return exitOf(cmd.OutOrStdout(), cmd.ErrOrStderr(), err)
	}

	return command
}

// exitOf maps a body error to its exit code. The lines a refusal carries go to stdout first.
func exitOf(stdout, stderr io.Writer, err error) error {
	if err == nil {
		return nil
	}

	var exit *ExitError
	if errors.As(err, &exit) {
		return err
	}

	say(stdout, carriedLines(err)...)

	if !isStopped(err) {
		say(stderr, err.Error())
	}

	return &ExitError{Code: exitProblems}
}

// carriedLines returns the lines a refusal or a stop carries.
func carriedLines(err error) []string {
	var refusal *checkpkg.Refusal
	if errors.As(err, &refusal) {
		return refusal.Lines
	}

	var stopped *brief.Stopped
	if errors.As(err, &stopped) {
		return stopped.Lines
	}

	return nil
}

// isStopped reports whether the error is a stop, which prints its lines and no message.
func isStopped(err error) bool {
	var stopped *brief.Stopped

	return errors.As(err, &stopped)
}

// say prints lines to a stream. A failed write has no recovery.
func say(out io.Writer, lines ...string) {
	for _, line := range lines {
		_, _ = fmt.Fprintln(out, line)
	}
}

// verdict returns an ExitError of 1 when ok is false, after printing the lines.
func verdict(out io.Writer, ok bool, lines ...string) error {
	say(out, lines...)

	if ok {
		return nil
	}

	return &ExitError{Code: exitError}
}

// group builds a command that holds the commands of its specs.
func group(use, short string, commands []*cobra.Command) *cobra.Command {
	parent := &cobra.Command{Use: use, Short: short}
	parent.AddCommand(commands...)

	return parent
}
