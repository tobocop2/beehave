package remedy

import (
	"cmp"
	"context"
	"flag"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/examples/remediation/tools/transcript"
)

const spendSyntax = "spend <transcript> [--hours <n>] [--cap <tokens>]"

// overVerdict ends the row of a lane that is over the cap.
const overVerdict = "\tOVER"

// spendCommands are the subcommands that read the spend of the lanes from a transcript.
func spendCommands() []command {
	return []command{
		{
			name:    "spend",
			usage:   spendSyntax,
			summary: "show the calls and the lane tokens of each hour of a transcript, then one row for each lane with its tokens and whether it is over the cap; --hours limits the table to the last hours",
			run:     spendCommand,
		},
	}
}

// spendFlags are the values of the flags of spend.
type spendFlags struct {
	hours   int
	capText string
}

func (f *spendFlags) declare(flags *flag.FlagSet) {
	flags.IntVar(&f.hours, "hours", 0, "show the last n hours of the table; every hour when not given")
	flags.StringVar(&f.capText, "cap", "", "the lane context cap in tokens; the default when not given")
}

// laneCap is the cap in tokens: the default when the flag is not given, a refusal when it is not a whole number above zero.
func (f *spendFlags) laneCap() (int64, error) {
	if f.capText == "" {
		return transcript.LaneCap(nil)
	}

	return transcript.LaneCap(&f.capText)
}

// spendCommand prints the hour table and the lane rows of a transcript. A lane over the cap is exit code 1. A bad flag, a bad cap and a transcript that cannot be read are exit code 2.
func spendCommand(_ context.Context, _ Deps, s streams, args []string) int {
	var given spendFlags

	path, ok := parseSubject("spend", spendSyntax, args, s.err, given.declare)
	if !ok {
		return ExitRefused
	}

	capTokens, err := given.laneCap()
	if err != nil {
		return refused(s, "spend", err)
	}

	lines, over, err := spendLines(path, given.hours, capTokens)
	if err != nil {
		return refused(s, "spend", err)
	}

	return showLines(s, lines, over)
}

// spendLines gives the hour table, then one row for each lane, and whether a lane is over the cap.
func spendLines(path string, hours int, capTokens int64) ([]string, bool, error) {
	// Both reads scan the same file, so they fail together; the first error is the one to show.
	table, tableErr := transcript.HoursTable(path, hours)
	rows, _, rowsErr := transcript.LaneRows(path, capTokens, nil)

	if err := cmp.Or(tableErr, rowsErr); err != nil {
		return nil, false, err
	}

	over := slices.ContainsFunc(rows, func(row string) bool { return strings.HasSuffix(row, overVerdict) })

	return append(table, rows...), over, nil
}
