package remedy

import (
	"context"
	"flag"
	"io"
	"strings"

	"github.com/tobocop2/beehave/examples/remediation/tools/roundcut"
)

// defaultTrunk is the trunk branch when --trunk is not given.
const defaultTrunk = "main"

const cutUsage = "usage: remedy cut --old <branch> --new <branch> --pr <number> [--trunk <branch>] [--dry]\n"

// cutCommand cuts the next round. A refusal of the cut is exit code 2, and so is a bad argument.
func cutCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	req, ok := parseCut(args, s.err)
	if !ok {
		return ExitRefused
	}

	result, err := cutRound(ctx, deps, req)
	if err != nil {
		say(s.err, "remedy cut: %v\n", err)

		return ExitRefused
	}

	return showCut(s, result)
}

// showCut prints the lines of the cut and gives its exit code.
func showCut(s streams, result roundcut.Result) int {
	for _, line := range result.Lines {
		say(s.out, "%s\n", line)
	}

	if result.Stop == roundcut.Refused {
		return ExitRefused
	}

	return ExitPass
}

// cutRound opens the forge of the origin remote and runs the cut on it.
func cutRound(ctx context.Context, deps Deps, req roundcut.Request) (roundcut.Result, error) {
	repo, err := originRepo(ctx, deps)
	if err != nil {
		return roundcut.Result{}, err
	}

	forge, err := deps.Forge(repo)
	if err != nil {
		return roundcut.Result{}, err
	}

	return roundcut.Run(ctx, forge, req), nil
}

// cutFlags are the values of the flags of cut.
type cutFlags struct {
	oldBase, newBase, trunk string
	pr                      int
	dry                     bool
}

func (f *cutFlags) declare(flags *flag.FlagSet) {
	flags.StringVar(&f.oldBase, "old", "", "the old round branch")
	flags.StringVar(&f.newBase, "new", "", "the new round branch")
	flags.StringVar(&f.trunk, "trunk", defaultTrunk, "the trunk branch")
	flags.IntVar(&f.pr, "pr", 0, "the number of the integration pull request")
	flags.BoolVar(&f.dry, "dry", false, "say what each write would do and write nothing")
}

// request is the cut that the flags ask for. The number of the new round is the text after the last dash of the new branch.
func (f *cutFlags) request() (roundcut.Request, bool) {
	roman := f.newBase[strings.LastIndex(f.newBase, "-")+1:]
	if f.oldBase == "" || roman == "" || f.pr <= 0 {
		return roundcut.Request{}, false
	}

	return roundcut.Request{
		Cut: roundcut.Cut{Roman: roman, OldPR: f.pr, OldBase: f.oldBase, NewBase: f.newBase, Trunk: f.trunk},
		Dry: f.dry,
	}, true
}

// parseCut reads the arguments of cut. It shows the usage and gives false for a bad argument.
func parseCut(args []string, errOut io.Writer) (roundcut.Request, bool) {
	flags := flag.NewFlagSet("remedy cut", flag.ContinueOnError)
	flags.SetOutput(errOut)

	var given cutFlags

	given.declare(flags)

	if flags.Parse(args) != nil {
		return roundcut.Request{}, false
	}

	req, ok := given.request()
	if !ok || flags.NArg() > 0 {
		say(errOut, cutUsage)

		return roundcut.Request{}, false
	}

	return req, true
}
