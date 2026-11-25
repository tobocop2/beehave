package remedy

import (
	"context"
	"flag"
	"io"
	"strings"

	"github.com/tobocop2/beehave/examples/remediation/tools/brief"
	"github.com/tobocop2/beehave/examples/remediation/tools/gate"
)

const reportSyntax = "report <file> --head <sha>"

// gateCommands are the subcommands that judge the report of a lane.
func gateCommands() []command {
	return []command{
		{
			name:    "report",
			usage:   reportSyntax,
			summary: "check the four tables of a lane report and that the report names the head commit",
			run:     reportCommand,
		},
	}
}

// printable keeps the bytes that are not UTF-8 out of a printed line.
func printable(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.ToValidUTF8(line, "�")
	}

	return out
}

// flagGiven reports whether a required flag has a value. With none, it shows the usage.
func flagGiven(errOut io.Writer, syntax, value string) bool {
	if value == "" {
		say(errOut, "usage: remedy %s\n", syntax)
	}

	return value != ""
}

// reportFlags are the values of the flags of report.
type reportFlags struct {
	head string
}

func (f *reportFlags) declare(flags *flag.FlagSet) {
	flags.StringVar(&f.head, "head", "", "the commit that the report must name")
}

// reportCommand checks the report of a lane in a file. A defect in the report is exit code 1. A missing --head and a file that cannot be read are exit code 2.
func reportCommand(_ context.Context, _ Deps, s streams, args []string) int {
	var given reportFlags

	path, ok := parseSubject("report", reportSyntax, args, s.err, given.declare)
	if !ok || !flagGiven(s.err, reportSyntax, given.head) {
		return ExitRefused
	}

	text, err := brief.ReadText(path)
	if err != nil {
		return refused(s, "report", err)
	}

	result := gate.Report(text, given.head)
	if result.OK() {
		return showLines(s, []string{result.Summary(given.head)}, false)
	}

	return showLines(s, printable(result.Failures), true)
}
