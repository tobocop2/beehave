// Package remedy is the command line of the remediation tools. One binary holds one subcommand for each tool, and the command table below is the list of them.
//
// A subcommand follows the exit codes of obee: 0 for a pass, 1 for a defect found and 2 for a refusal or a failed read. A subcommand prints its findings to the standard output, one line each.
package remedy

import (
	"context"
	"fmt"
	"io"

	"github.com/tobocop2/obee/examples/remediation/tools/roundcut"
	"github.com/tobocop2/obee/internal/proc"
)

// The exit codes of every subcommand.
const (
	ExitPass    = 0
	ExitDefect  = 1
	ExitRefused = 2
)

// Deps holds what the subcommands read from outside. The binary builds them, and a test passes fakes.
// Git runs git in Dir, the working folder. Forge opens the forge of the repository that the origin remote of Dir names.
type Deps struct {
	Git   proc.Runner
	Dir   string
	Forge func(repo string) (roundcut.Forge, error)
}

// streams are the two outputs of a run.
type streams struct {
	out, err io.Writer
}

// command is one subcommand: its name, a line of help and the function that runs it.
type command struct {
	name    string
	usage   string
	summary string
	run     func(ctx context.Context, deps Deps, s streams, args []string) int
}

// commands is the command table. The binary runs from it, `remedy --help` prints from it, and the test of workflow.hcl reads it.
func commands() []command {
	return []command{
		{
			name:    "cut",
			usage:   "cut --old <branch> --new <branch> --pr <number> [--trunk <branch>] [--dry]",
			summary: "cut the next round branch after the integration pull request merged",
			run:     cutCommand,
		},
		{
			name:    "audit",
			usage:   "audit authored <span> | audit present <tip> <base>",
			summary: "read the merges of a range for content that no pull request diff shows",
			run:     auditCommand,
		},
	}
}

// Names returns the name of each subcommand, in table order.
func Names() []string {
	names := []string{}

	for _, c := range commands() {
		names = append(names, c.name)
	}

	return names
}

// Execute runs one command line (without the program name) and returns the exit code.
func Execute(ctx context.Context, args []string, deps Deps, out, errOut io.Writer) int {
	s := streams{out: out, err: errOut}

	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return help(s, args)
	}

	for _, c := range commands() {
		if c.name == args[0] {
			return c.run(ctx, deps, s, args[1:])
		}
	}

	say(errOut, "remedy: unknown command %q\n", args[0])
	usage(errOut)

	return ExitRefused
}

// help shows the usage. A request for help passes. A call with no command shows it on the error output and is a refusal.
func help(s streams, args []string) int {
	if len(args) == 0 {
		usage(s.err)

		return ExitRefused
	}

	usage(s.out)

	return ExitPass
}

func usage(w io.Writer) {
	say(w, "usage: remedy <command> [arguments]\n\n")

	for _, c := range commands() {
		say(w, "  %s\n      %s\n", c.usage, c.summary)
	}
}

func say(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, format, args...)
}
