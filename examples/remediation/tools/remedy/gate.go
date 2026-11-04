package remedy

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/examples/remediation/tools/brief"
	"github.com/tobocop2/beehave/examples/remediation/tools/gate"
	"github.com/tobocop2/beehave/examples/remediation/tools/job"
)

const (
	gatelistSyntax = "gatelist <file>"
	jobSyntax      = "job <script> --list <file> [--job-dir <folder>]"
	reportSyntax   = "report <file> --head <sha>"
)

// gateCommands are the subcommands that judge the gate list, a host job and the report of a lane.
func gateCommands() []command {
	return []command{
		{
			name:    "gatelist",
			usage:   gatelistSyntax,
			summary: "read the '## Gate steps' table of a gate list and refuse a list that states no usable step",
			run:     gatelistCommand,
		},
		{
			name:    "job",
			usage:   jobSyntax,
			summary: "check a host job script against the gate list: every step runs or declares a skip, tests have a timeout, no bulk staging, and the final marker prints a real exit status; --job-dir is the folder that a sourced file is read from, the folder of the script when not given",
			run:     jobCommand,
		},
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

// gatelistCommand reads the gate list in a file. A list that states no usable step, and a file that cannot be read, are exit code 2.
func gatelistCommand(_ context.Context, _ Deps, s streams, args []string) int {
	path, ok := parseSubject("gatelist", gatelistSyntax, args, s.err, func(*flag.FlagSet) {})
	if !ok {
		return ExitRefused
	}

	text, err := brief.ReadText(path)
	if err != nil {
		return refused(s, "gatelist", err)
	}

	steps, err := gate.Steps(text, path)
	if err != nil {
		return refused(s, "gatelist", err)
	}

	return showLines(s, []string{fmt.Sprintf("check-gatelist: OK (steps %d)", len(steps))}, false)
}

// jobFlags are the values of the flags of job.
type jobFlags struct {
	list, dir string
}

func (f *jobFlags) declare(flags *flag.FlagSet) {
	flags.StringVar(&f.list, "list", "", "the gate list that the job must follow")
	flags.StringVar(&f.dir, "job-dir", "", "the folder that a file the job sources is read from; the folder of the script when not given")
}

// flagGiven reports whether a required flag has a value. With none, it shows the usage.
func flagGiven(errOut io.Writer, syntax, value string) bool {
	if value == "" {
		say(errOut, "usage: remedy %s\n", syntax)
	}

	return value != ""
}

// readTexts reads each file, and gives the refusal of the first file that cannot be read.
func readTexts(paths ...string) ([]string, error) {
	texts := make([]string, len(paths))

	for i, path := range paths {
		text, err := brief.ReadText(path)
		if err != nil {
			return nil, err
		}

		texts[i] = text
	}

	return texts, nil
}

// readSource reads a file that the job sources, and gives false when it cannot be read.
func readSource(path string) (string, bool) {
	text, err := brief.ReadText(path)

	return text, err == nil
}

// jobCommand checks a job script against the gate list. A defect in the job is exit code 1. A gate list that cannot be read, a missing --list and a file that cannot be read are exit code 2.
func jobCommand(_ context.Context, _ Deps, s streams, args []string) int {
	var given jobFlags

	script, ok := parseSubject("job", jobSyntax, args, s.err, given.declare)
	if !ok || !flagGiven(s.err, jobSyntax, given.list) {
		return ExitRefused
	}

	texts, err := readTexts(script, given.list)
	if err != nil {
		return refused(s, "job", err)
	}

	return checkJob(s, texts[0], texts[1], given.list, sourceDir(given.dir, script))
}

// checkJob judges the text of a job script against the text of the gate list. A gate list that cannot be read keeps the lines of the job script first.
func checkJob(s streams, script, list, listPath, dir string) int {
	own := job.Script(script)

	result, err := gate.Job(script, list, listPath, dir, readSource)
	if err != nil {
		show(s.out, printable(own))

		return refused(s, "job", err)
	}

	lines, defect := jobLines(own, result)

	return showLines(s, printable(lines), defect)
}

// jobLines joins the lines of the job script and of the gate comparison. A job with no defect ends with the summary.
func jobLines(own []string, result gate.Result) ([]string, bool) {
	lines := slices.Concat(own, result.Lines)
	defect := len(own) > 0 || !result.OK()

	if !defect {
		lines = append(lines, result.Summary())
	}

	return lines, defect
}

// sourceDir is the folder that a sourced file is read from: the one that is given, else the folder of the script.
func sourceDir(dir, script string) string {
	if dir != "" {
		return dir
	}

	return filepath.Dir(script)
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

	texts, err := readTexts(path)
	if err != nil {
		return refused(s, "report", err)
	}

	result := gate.Report(texts[0], given.head)
	if result.OK() {
		return showLines(s, []string{result.Summary(given.head)}, false)
	}

	return showLines(s, printable(result.Failures), true)
}
