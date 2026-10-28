package model

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
