package model

import "strings"

// CommandResult is what one call of an outside command (bd or gh) returned.
type CommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

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
