package remedy

import (
	"context"
	"flag"
	"io"

	"github.com/tobocop2/beehave/internal/roundintegration"
)

const integrationSyntax = "integration --base <round branch> --into <trunk> [--pr <number>] [--label <name>]"

func parseIntegration(args []string, errOut io.Writer) (roundintegration.Request, bool) {
	var given roundintegration.Request

	flags := flag.NewFlagSet("remedy integration", flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.StringVar(&given.Base, "base", "", "the round branch: the head of the integration pull request")
	flags.StringVar(&given.Into, "into", "", "the trunk: the base of the integration pull request")
	flags.StringVar(&given.Label, "label", defaultLabel, "the label of the integration pull request when the tool opens it; empty adds none")
	flags.IntVar(&given.PR, "pr", 0, "the number of the integration pull request; 0 finds it, and opens it when there is none")

	if flags.Parse(args) != nil || flags.NArg() > 0 || given.Base == "" || given.Into == "" || given.PR < 0 {
		say(errOut, "usage: remedy %s\n", integrationSyntax)

		return given, false
	}

	return given, true
}

// integrationCommand finds the integration pull request, or opens it, writes its body again from the pull requests that merged into the round branch,
// and prints `pr=<n> state=<state>`. A failed read or write is exit code 2.
func integrationCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	given, ok := parseIntegration(args, s.err)
	if !ok {
		return ExitRefused
	}

	result, err := integrate(ctx, deps, given)
	if err != nil {
		return refused(s, "integration", err)
	}

	say(s.out, "pr=%d state=%s\n", result.PR, result.State)

	return ExitPass
}

// integrate opens the forge of the origin remote and runs the integration on it.
func integrate(ctx context.Context, deps Deps, given roundintegration.Request) (roundintegration.Result, error) {
	repo, err := originRepo(ctx, deps)
	if err != nil {
		return roundintegration.Result{}, err
	}

	opened, err := deps.Forge(repo)
	if err != nil {
		return roundintegration.Result{}, err
	}

	forge, ok := opened.(roundintegration.Forge)
	if !ok {
		return roundintegration.Result{}, errNoIntegrator
	}

	return roundintegration.Run(ctx, forge, given)
}
