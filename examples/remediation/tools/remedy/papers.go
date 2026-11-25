package remedy

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"io"

	"github.com/tobocop2/beehave/examples/remediation/tools/brief"
	"github.com/tobocop2/beehave/examples/remediation/tools/check"
	"github.com/tobocop2/beehave/examples/remediation/tools/papers"
)

const (
	handoffSyntax = "handoff <file> [--name <text>] [--transcript <file>] [--cap <tokens>]"
	authorsSyntax = "authors <file> --email <address> --login <name>"

	// authorsWrongAddress is the code that the authors check gives for a commit of the owner under another address.
	authorsWrongAddress = 3
)

// papersCommands are the subcommands that judge what a papers lane leaves: its handoff and its commit authors.
func papersCommands() []command {
	return []command{
		{
			name:    "handoff",
			usage:   handoffSyntax,
			summary: "check the handoff of a lane: the regression count, the tokens at handoff and a launched job that the lane did not read",
			run:     handoffCommand,
		},
		{
			name:    "authors",
			usage:   authorsSyntax,
			summary: "check that each commit of the owner carries the address of the owner; the file holds one '<sha> <email> <login>' line for each commit",
			run:     authorsCommand,
		},
	}
}

// parseSubject reads the one positional argument of a subcommand and the flags after it. It shows the usage and gives false for a missing argument, a bad flag or an extra argument.
func parseSubject(name, syntax string, args []string, errOut io.Writer, declare func(*flag.FlagSet)) (string, bool) {
	if len(args) == 0 {
		return wrongUsage(errOut, syntax)
	}

	flags := flag.NewFlagSet("remedy "+name, flag.ContinueOnError)
	flags.SetOutput(errOut)
	declare(flags)

	if flags.Parse(args[1:]) != nil {
		return "", false
	}

	if flags.NArg() > 0 {
		return wrongUsage(errOut, syntax)
	}

	return args[0], true
}

// wrongUsage shows the usage and gives false.
func wrongUsage(errOut io.Writer, syntax string) (string, bool) {
	say(errOut, "usage: remedy %s\n", syntax)

	return "", false
}

// show prints the lines, one each.
func show(w io.Writer, lines []string) {
	for _, line := range lines {
		say(w, "%s\n", line)
	}
}

// showLines prints the lines to the standard output and gives exit code 1 for a defect and 0 for none.
func showLines(s streams, lines []string, defect bool) int {
	show(s.out, lines)

	if defect {
		return ExitDefect
	}

	return ExitPass
}

// refused prints the lines that a refusal of a check carries and its message, and gives exit code 2.
func refused(s streams, name string, err error) int {
	var refusal *check.Refusal
	if errors.As(err, &refusal) {
		show(s.out, refusal.Lines)
	}

	say(s.err, "remedy %s: %v\n", name, err)

	return ExitRefused
}

// handoffFlags are the values of the flags of handoff.
type handoffFlags struct {
	name, transcript, capText string
}

func (f *handoffFlags) declare(flags *flag.FlagSet) {
	flags.StringVar(&f.name, "name", "", "how the handoff is called in messages; the path of the file when not given")
	flags.StringVar(&f.transcript, "transcript", "", "the transcript of the lane, read when the handoff holds no token count")
	flags.StringVar(&f.capText, "cap", "", "the context cap in tokens; the default when not given")
}

// options are the options of the handoff check. A cap that is not given is the default of the check.
func (f *handoffFlags) options() papers.HandoffOptions {
	opts := papers.HandoffOptions{Transcript: f.transcript}
	if f.capText != "" {
		opts.Cap = &f.capText
	}

	return opts
}

// handoffCommand checks the handoff in a file. A refused handoff is exit code 1. A file that cannot be read, a bad cap and a transcript that cannot be read are exit code 2.
func handoffCommand(_ context.Context, _ Deps, s streams, args []string) int {
	var given handoffFlags

	path, ok := parseSubject("handoff", handoffSyntax, args, s.err, given.declare)
	if !ok {
		return ExitRefused
	}

	text, err := brief.ReadText(path)
	if err != nil {
		return refused(s, "handoff", err)
	}

	result, err := papers.Handoff(text, cmp.Or(given.name, path), given.options())
	if err != nil {
		return refused(s, "handoff", err)
	}

	return showLines(s, result.Lines, result.Refused)
}

// authorsFlags are the values of the flags of authors.
type authorsFlags struct {
	email, login string
}

func (f *authorsFlags) declare(flags *flag.FlagSet) {
	flags.StringVar(&f.email, "email", "", "the address that a commit of the owner must carry")
	flags.StringVar(&f.login, "login", "", "the login that tells which commits are the owner's")
}

// authorsCommand checks the commit authors in a file against the identity of the owner. A commit of the owner under another address is exit code 1. A missing identity, an empty file and a failed read are exit code 2.
func authorsCommand(_ context.Context, _ Deps, s streams, args []string) int {
	var given authorsFlags

	path, ok := parseSubject("authors", authorsSyntax, args, s.err, given.declare)
	if !ok {
		return ExitRefused
	}

	text, err := brief.ReadText(path)
	if err != nil {
		return refused(s, "authors", err)
	}

	result := papers.Authors(text, given.email, given.login)

	show(s.out, result.Out)
	show(s.err, result.Err)

	return authorsExit(result.Code)
}

// authorsExit maps the codes of the authors check to the exit codes of every subcommand: its wrong address is a defect found, and its other refusal is a refusal.
func authorsExit(code int) int {
	switch code {
	case 0:
		return ExitPass
	case authorsWrongAddress:
		return ExitDefect
	}

	return ExitRefused
}
