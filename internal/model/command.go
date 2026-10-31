package model

import "strings"

// CommandResult is what one call of an outside command (bd or gh) returned.
type CommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// SetupErr is set when the runner could not set up the call (a process cap that cannot be counted or set). The command did not start. It is never set from the output of the command.
	SetupErr error
}

// ReportLimit is the most bytes of command output that obee keeps. The tail of an output holds the findings.
const ReportLimit = 16 * 1024

// IsDigits reports whether text is a non-empty run of ASCII digits.
func IsDigits(text string) bool {
	if text == "" {
		return false
	}

	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

// IsRepoPart reports whether text is a run of letters, digits, dots, underscores and hyphens. It is not empty, ".", or "..".
func IsRepoPart(text string) bool {
	return text != "" && text != "." && text != ".." && strings.IndexFunc(text, notRepoRune) < 0
}

func notRepoRune(r rune) bool {
	return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-'
}
