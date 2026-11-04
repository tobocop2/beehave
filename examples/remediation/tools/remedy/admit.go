package remedy

import (
	"context"
	"errors"
	"flag"
	"io"
	"strconv"

	"github.com/tobocop2/beehave/examples/remediation/tools/brief"
	"github.com/tobocop2/beehave/examples/remediation/tools/papers"
)

const admitSyntax = "admit <kind> --lane <name> --key <pr> --state <folder> [--lane-kind fix|rework|relay] [--mechanism <token>] [--acceptance <path>] [--base-result <path>] [--new-behavior] [--handoff <file>] [--transcript <file>] [--cap <tokens>]"

// OpenBook turns the opener of a lane ledger into the Book field of Deps.
func OpenBook[B Book](open func(dir string) B) func(string) Book {
	return func(dir string) Book {
		return open(dir)
	}
}

// handoffCheck judges the handoff of the lane before this one with the handoff check of the papers lane.
func handoffCheck(text, name, transcript string, tokens int) ([]string, bool, error) {
	capText := strconv.Itoa(tokens)

	result, err := papers.Handoff(text, name, papers.HandoffOptions{Transcript: transcript, Cap: &capText})

	return result.Lines, result.Refused, err
}

// admitFlags are the values of the flags of admit.
type admitFlags struct {
	lane, key, state, laneKind, mechanism string
	acceptance, baseResult                string
	handoff, transcript                   string
	cap                                   int
	newBehavior                           bool
}

func (f *admitFlags) declare(flags *flag.FlagSet) {
	flags.StringVar(&f.lane, "lane", "", "the name of the lane")
	flags.StringVar(&f.key, "key", "", "the pull request number, or the item in the queue")
	flags.StringVar(&f.state, "state", "", "the state folder that holds the ledger")
	flags.StringVar(&f.laneKind, "lane-kind", "", "fix, rework or relay; fix when not given")
	flags.StringVar(&f.mechanism, "mechanism", "", "the code path that the lane patches, one token")
	flags.StringVar(&f.acceptance, "acceptance", "", "the path of the acceptance script, or none: <reason>")
	flags.StringVar(&f.baseResult, "base-result", "", "the path of the result at the base, or none: <reason>")
	flags.StringVar(&f.handoff, "handoff", "", "the handoff of the lane before this one")
	flags.StringVar(&f.transcript, "transcript", "", "the transcript of the lane before this one")
	flags.IntVar(&f.cap, "cap", 0, "the context cap in tokens; the default when not given")
	flags.BoolVar(&f.newBehavior, "new-behavior", false, "the lane adds new behavior")
}

func (f *admitFlags) spec() brief.Lane {
	return brief.Lane{
		Key: f.key, Kind: brief.LaneKind(f.laneKind), Mechanism: f.mechanism, Acceptance: f.acceptance, BaseResult: f.baseResult,
		NewBehavior: f.newBehavior, Handoff: f.handoff, Transcript: f.transcript, Cap: f.cap,
	}
}

// parseAdmit reads the kind and the flags of admit. It shows the usage and gives false for a bad argument.
func parseAdmit(args []string, errOut io.Writer) (brief.Kind, admitFlags, bool) {
	if len(args) == 0 {
		return "", admitFlags{}, admitUsage(errOut)
	}

	kind, err := brief.ParseKind(args[0])
	if err != nil {
		say(errOut, "remedy admit: %v\n", err)

		return "", admitFlags{}, false
	}

	given, ok := readAdmitFlags(args[1:], errOut)

	return kind, given, ok
}

func readAdmitFlags(args []string, errOut io.Writer) (admitFlags, bool) {
	var given admitFlags

	flags := flag.NewFlagSet("remedy admit", flag.ContinueOnError)
	flags.SetOutput(errOut)
	given.declare(flags)

	if flags.Parse(args) != nil {
		return given, false
	}

	if given.lane == "" || given.state == "" || flags.NArg() > 0 {
		return given, admitUsage(errOut)
	}

	return given, true
}

func admitUsage(errOut io.Writer) bool {
	say(errOut, "usage: remedy %s\n", admitSyntax)

	return false
}

// admitCommand runs the admission gates of a lane against the ledger. A hold-back with lines for the orchestrator is exit code 1. A refusal and a failed read are exit code 2.
func admitCommand(_ context.Context, deps Deps, s streams, args []string) int {
	kind, given, ok := parseAdmit(args, s.err)
	if !ok {
		return ExitRefused
	}

	admission, err := brief.Admit(kind, given.lane, given.spec(), deps.Book(given.state), handoffCheck)

	return showAdmit(s, admission, err)
}

// showAdmit prints what the admission found and gives its exit code.
func showAdmit(s streams, admission brief.Admission, err error) int {
	var stop *brief.Stopped

	switch {
	case err == nil:
		say(s.out, "ADMITTED kind=%s mechanism=%s\n", admission.Kind, admission.Mechanism)

		return ExitPass
	case errors.As(err, &stop):
		for _, line := range stop.Lines {
			say(s.out, "%s\n", line)
		}

		return ExitDefect
	default:
		say(s.err, "remedy admit: %v\n", err)

		return ExitRefused
	}
}
