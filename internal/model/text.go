package model

import "strings"

// Records splits on newline only, so a carriage return stays in its line, and drops the empty record after the last newline.
func Records(text string) []string {
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// BlankFields splits on spaces, tabs and newlines only, as awk does.
func BlankFields(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
}

// FlatText joins the lines of a command and folds each run of blanks to one blank.
func FlatText(text string) string {
	return strings.Join(BlankFields(strings.ReplaceAll(text, "\\\n", " ")), " ")
}

// IsAlnum reports whether the byte is an ASCII letter or digit.
func IsAlnum(char byte) bool {
	return char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
}

// HoldsToken reports whether the text holds the token with no word byte after it, and, when lead is set, none before it.
func HoldsToken(text, token string, lead bool, word func(byte) bool) bool {
	for from := 0; from < len(text); {
		at := strings.Index(text[from:], token)
		if at < 0 {
			return false
		}

		at += from
		end := at + len(token)

		if (!lead || at == 0 || !word(text[at-1])) && (end == len(text) || !word(text[end])) {
			return true
		}

		from = at + 1
	}

	return false
}
