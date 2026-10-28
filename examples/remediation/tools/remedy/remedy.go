// Package remedy is the command line of the remediation tools. One binary holds one subcommand for each tool, and the command table below is the list of them.
//
// A subcommand follows the exit codes of obee: 0 for a pass, 1 for a defect found and 2 for a refusal or a failed read. A subcommand prints its findings to the standard output, one line each.
package remedy

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tobocop2/obee/examples/remediation/tools/brief"
	"github.com/tobocop2/obee/examples/remediation/tools/roundcut"
	"github.com/tobocop2/obee/examples/remediation/tools/takeover"
	"github.com/tobocop2/obee/internal/proc"
)

// The exit codes of every subcommand.
const (
	ExitPass    = 0
	ExitDefect  = 1
	ExitRefused = 2
)

// Forge is the forge of every subcommand: the calls of the round cut and the calls of the takeover.
type Forge interface {
	roundcut.Forge
	takeover.Reader
}

// Claims is the claim store of obee: the one call of the claim that the takeover needs.
// EnsureFree refuses when another owner holds the item, and writes nothing. The tools write no claim: the run of an item in the engine holds it.
type Claims interface {
	EnsureFree(ctx context.Context, owner, item string) error
}

// Deps holds what the subcommands read from outside. The binary builds them, and a test passes fakes.
// Git runs git in Dir, the working folder. Forge opens the forge of the repository that the origin remote of Dir names. Now gives the time.
// Claims opens the claim store of the project that owns the repository. A step opens it only when it reads the claim, after the guards that need no store.
// Book opens the lane ledger in a state folder.
type Deps struct {
	Git    proc.Runner
	Dir    string
	Forge  func(repo string) (Forge, error)
	Now    func() time.Time
	Claims func(repo string) (Claims, error)
	Book   func(dir string) brief.Book
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
	return append(roundCommands(), briefCommands()...)
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

// roundCommands are the subcommands that cut a round, audit merges and decide a takeover.
func roundCommands() []command {
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
		{
			name:    "take",
			usage:   takeSyntax,
			summary: "decide whether to take a pull request of somebody else: a dry read, --apply posts the notice, --take checks the grace period",
			run:     takeCommand,
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
