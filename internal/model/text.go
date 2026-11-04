package model

import "strings"

// Records splits on newline only, so a carriage return stays in its line, and drops the empty record after the last newline.
func Records(text string) []string {
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// NonEmptyLines returns the lines of the text that hold more than blanks, each trimmed.
func NonEmptyLines(text string) []string {
	var lines []string

	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}

	return lines
}

// FirstLine returns the first line of the text that holds more than blanks, trimmed.
func FirstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")

	return strings.TrimSpace(line)
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

// OneLine puts a text on one line: every run of white space becomes one space.
func OneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// Clip puts a text on one line and cuts it at limit characters. A cut text ends with mark.
func Clip(text string, limit int, mark string) string {
	text = OneLine(text)
	if cut := FirstRunes(text, limit); cut != text {
		return cut + mark
	}

	return text
}

// IsHomePath reports whether a path is written ~ or ~/...
func IsHomePath(path string) bool {
	return path == "~" || strings.HasPrefix(path, "~/")
}
