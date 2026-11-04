package remedy

import (
	"context"
	"flag"
	"io"

	"github.com/tobocop2/beehave/examples/remediation/tools/brief"
)

const sizeSyntax = "size <brief> [--cap <tokens>] [--root <folder>]"

// parseSize reads the brief and the flags of size. It shows the usage and gives false for a bad argument.
func parseSize(args []string, errOut io.Writer) (string, brief.SizeOptions, bool) {
	var opts brief.SizeOptions

	flags := flag.NewFlagSet("remedy size", flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.IntVar(&opts.Cap, "cap", 0, "the context cap in tokens; the default when not given")
	flags.StringVar(&opts.Root, "root", "", "the round folder that a <S>/ path names")

	if len(args) == 0 || flags.Parse(args[1:]) != nil {
		say(errOut, "usage: remedy %s\n", sizeSyntax)

		return "", opts, false
	}

	if flags.NArg() > 0 {
		say(errOut, "usage: remedy %s\n", sizeSyntax)

		return "", opts, false
	}

	return args[0], opts, true
}

// sizeCommand prints the token table of a brief, then the check of its required reading. A warning is exit code 1. A reading above the limit and a failed read are exit code 2.
func sizeCommand(_ context.Context, _ Deps, s streams, args []string) int {
	name, opts, ok := parseSize(args, s.err)
	if !ok {
		return ExitRefused
	}

	table, err := brief.Size(name, opts)
	if err != nil {
		return sizeRefused(s, err)
	}

	checked, err := brief.Check(name, opts)

	return sizeExit(s, err, showReport(s, table)+showReport(s, checked))
}

// sizeExit gives the exit code of a size run: a refusal of the check, then a warning, then a pass.
func sizeExit(s streams, err error, warned int) int {
	switch {
	case err != nil:
		return sizeRefused(s, err)
	case warned > 0:
		return ExitDefect
	}

	return ExitPass
}

func sizeRefused(s streams, err error) int {
	say(s.err, "remedy size: %v\n", err)

	return ExitRefused
}

// showReport prints the lines of a report and returns the number of warnings in it.
func showReport(s streams, report brief.Report) int {
	for _, line := range report.Out {
		say(s.out, "%s\n", line)
	}

	for _, line := range report.Warn {
		say(s.out, "%s\n", line)
	}

	return len(report.Warn)
}
