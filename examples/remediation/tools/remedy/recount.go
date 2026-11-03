package remedy

import (
	"context"
	"flag"
	"io"
	"strings"

	"github.com/tobocop2/obee/examples/remediation/tools/recount"
	"github.com/tobocop2/obee/internal/model"
)

const recountSyntax = "recount --pr <number> [--was-rows <n>] [--was-issues <n>]"

// recountFlags are the values of the flags of recount. A was value of -1 means the flag was not given.
type recountFlags struct {
	pr                 int
	wasRows, wasIssues int
}

func parseRecount(args []string, errOut io.Writer) (recountFlags, bool) {
	given := recountFlags{wasRows: -1, wasIssues: -1}

	flags := flag.NewFlagSet("remedy recount", flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.IntVar(&given.pr, "pr", 0, "the number of the integration pull request")
	flags.IntVar(&given.wasRows, "was-rows", -1, "the row count before the write: the count now must differ")
	flags.IntVar(&given.wasIssues, "was-issues", -1, "the issue count before the write: the count now must differ")

	if flags.Parse(args) != nil || flags.NArg() > 0 || given.pr <= 0 || negativeWas(flags) {
		say(errOut, "usage: remedy %s\n", recountSyntax)

		return given, false
	}

	return given, true
}

// negativeWas reports a negative flag value on the command line. -1 is the value of a flag that was not given, so a given -1 would turn the assert off.
func negativeWas(flags *flag.FlagSet) bool {
	negative := false

	flags.Visit(func(f *flag.Flag) {
		if strings.HasPrefix(f.Value.String(), "-") {
			negative = true
		}
	})

	return negative
}

// readIntegration reads the title and the body of the pull request from the forge of the origin remote.
func readIntegration(ctx context.Context, deps Deps, pr int) (model.PullRequest, error) {
	repo, err := originRepo(ctx, deps)
	if err != nil {
		return model.PullRequest{}, err
	}

	forge, err := deps.Forge(repo)
	if err != nil {
		return model.PullRequest{}, err
	}

	return forge.PRView(ctx, pr, "title", "body")
}

// recountCommand reads the title and the body of the integration pull request, prints the counts of its rows as `rows=<n> issues=<n>`,
// and exits 1 when the heading or the title says another number, or when a count that the flags name is the same as before the write.
// A failed read is exit code 2.
func recountCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	given, ok := parseRecount(args, s.err)
	if !ok {
		return ExitRefused
	}

	pull, err := readIntegration(ctx, deps, given.pr)
	if err != nil {
		return refused(s, "recount", err)
	}

	now := recount.Count(pull.Body)
	say(s.out, "rows=%d issues=%d\n", now.Rows, now.Issues)

	return showFindings(s, append(recount.Check(pull.Title, pull.Body), given.moved(now)...))
}

// moved returns the findings for a count that the flags name and that is the same as before the write.
func (f recountFlags) moved(now recount.Tally) []string {
	return recount.Changed(now, recount.Tally{Rows: f.wasRows, Issues: f.wasIssues}, f.wasRows >= 0, f.wasIssues >= 0)
}

// showFindings prints each finding and gives exit code 1 when there is one.
func showFindings(s streams, found []string) int {
	for _, line := range found {
		say(s.out, "%s\n", line)
	}

	if len(found) > 0 {
		return ExitDefect
	}

	return ExitPass
}
