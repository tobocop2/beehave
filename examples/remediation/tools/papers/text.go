package papers

import "strings"

// recordsOf splits on newline only, so a carriage return stays in its line, and drops the empty record after the last newline.
func recordsOf(text string) []string {
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// blankFields splits on spaces, tabs and newlines only, as awk does.
func blankFields(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
}
