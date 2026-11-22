// Package remedy is the command line of the remediation tools. One binary holds one subcommand for each tool, and the command table below is the list of them.
//
// A subcommand follows the exit codes of beehave: 0 for a pass, 1 for a defect found and 2 for a refusal or a failed read. A subcommand prints its findings to the standard output, one line each.
package remedy

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/tobocop2/beehave/examples/remediation/tools/brief"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// The exit codes of every subcommand.
const (
	ExitPass    = 0
	ExitDefect  = 1
	ExitRefused = 2
)

// Forge is the forge of every subcommand: the title and body read of the recount, and the body read of the first review row.
// A forge that also has the closing-reference read of Listing gives the first review row the closing references of the forge for a pull request into the default branch. The body is read for any other pull request.
type Forge interface {
	bodyReader
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
}

// Book is the lane ledger of the subcommands: the reads and the write of admission, and the read of the issues of a first review.
type Book interface {
	brief.Book
	Closes(key string) ([]string, error)
}

// Deps holds what the subcommands read from outside. The binary builds them, and a test passes fakes.
// Git runs git in Dir, the working folder. Forge opens the forge of the repository that the origin remote of Dir names. Now gives the time.
// Book opens the lane ledger in the state folder that the --state flag of a subcommand names.
type Deps struct {
	Git   proc.Runner
	Dir   string
	Forge func(repo string) (Forge, error)
	Now   func() time.Time
	Book  func(dir string) Book
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

// commands is the command table. The binary runs from it, `remedy --help` prints from it, and the test of workflow.star reads it.
func commands() []command {
	return slices.Concat(briefCommands(), ledgerCommands(), papersCommands(), spendCommands(), gateCommands())
}

// ledgerCommands are the subcommands that keep the lane ledger and count its rows.
func ledgerCommands() []command {
	return []command{
		{
			name:    "ledger",
			usage:   ledgerSyntax,
			summary: "add a lane row to the ledger, or count its rows, patch rounds, earlier lanes and closed issues; the first review of a key records the issues its pull request closes",
			run:     ledgerCommand,
		},
		{
			name:    "recount",
			usage:   recountSyntax,
			summary: "count the rows of the integration pull request, and refuse a heading or title that says another number, or a count that did not change after a write",
			run:     recountCommand,
		},
	}
}

// briefCommands are the subcommands that judge a lane and its brief before the lane starts.
func briefCommands() []command {
	return []command{
		{
			name:    "admit",
			usage:   admitSyntax,
			summary: "check that the ledger lets a lane of this kind start: acceptance lines, design lane, patch rounds and the handoff of the lane before",
			run:     admitCommand,
		},
		{
			name:    "size",
			usage:   sizeSyntax,
			summary: "size the reading of a brief in tokens, warn about big files and refuse a reading above the budget",
			run:     sizeCommand,
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
