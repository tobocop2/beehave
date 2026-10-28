package job

import "strings"

// blankFields splits on spaces, tabs and newlines only, as awk does.
func blankFields(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
}

// flatText joins the lines of a command and folds each run of blanks to one blank.
func flatText(text string) string {
	return strings.Join(blankFields(strings.ReplaceAll(text, "\\\n", " ")), " ")
}
