package remedy

import (
	"context"
	"strings"

	"github.com/tobocop2/beehave/examples/remediation/tools/audit"
)

// reading is one report of the audit.
type reading func(ctx context.Context, a *audit.Audit) ([]audit.Line, error)

const auditUsage = "usage: remedy audit authored <span> | remedy audit present <tip> <base>\n"

// auditCommand reads the merges of a range. A line that marks a finding is exit code 1. A failed read is exit code 2.
func auditCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	read := auditRead(args)
	if read == nil {
		say(s.err, auditUsage)

		return ExitRefused
	}

	lines, err := readAudit(ctx, deps, read)
	if err != nil {
		say(s.err, "remedy audit: %v\n", err)

		return ExitRefused
	}

	return showAudit(s, lines)
}

// readAudit opens the working folder and runs the report on it.
func readAudit(ctx context.Context, deps Deps, read reading) ([]audit.Line, error) {
	reader, err := audit.Open(deps.Git, deps.Dir)
	if err != nil {
		return nil, err
	}

	return read(ctx, reader)
}

// auditRead picks the report that the arguments ask for. It is nil when they ask for none.
func auditRead(args []string) reading {
	switch {
	case len(args) == 2 && args[0] == "authored":
		return func(ctx context.Context, a *audit.Audit) ([]audit.Line, error) {
			return a.Authored(ctx, args[1])
		}
	case len(args) == 3 && args[0] == "present":
		return func(ctx context.Context, a *audit.Audit) ([]audit.Line, error) {
			return a.Present(ctx, args[1], args[2])
		}
	default:
		return nil
	}
}

// showAudit prints the lines and returns exit code 1 when one marks a finding.
func showAudit(s streams, lines []audit.Line) int {
	code := ExitPass

	for _, line := range lines {
		say(s.out, "%s\n", line)

		if marksDefect(line.String()) {
			code = ExitDefect
		}
	}

	return code
}

const (
	signalMarker  = "SIGNAL:"
	findingMarker = "<-- FINDING:"
)

// marksDefect says whether a line holds a marker of the audit at the place where the audit writes it. A path or a subject that holds the text of a marker does not count.
// A SIGNAL: marker starts an indented row. A FINDING: marker follows the arrow on the row of a merge, and that row starts with the # of its label.
func marksDefect(text string) bool {
	for _, row := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(row, " "), signalMarker) {
			return true
		}

		if strings.HasPrefix(row, "#") && strings.Contains(row, findingMarker) {
			return true
		}
	}

	return false
}
