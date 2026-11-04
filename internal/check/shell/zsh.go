package shell

import (
	"regexp"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	maxFormText = 40
	// markerName starts the parameter name that stands in for a zsh-only form while the text is parsed.
	markerName = "BEEHAVEZSH"
)

var (
	// zshForm matches the start of a zsh-only expansion: `${`, then a flag group `(...)` or one of `=`, `~` and `^`.
	zshForm = regexp.MustCompile(`\$\{(?:\([^)'"}\s]*\)|[(=~^])`)
	// markerRef matches a stand-in name, in a node or in comment text, and captures its number.
	markerRef = regexp.MustCompile(`\$\{` + markerName + `(\d+)_`)
)

// zshHint returns what a zsh-only expansion is and how to write the same thing in POSIX, for the character after `${`: one of `(`, `=`, `~` or `^`.
func zshHint(c byte) string {
	switch c {
	case '(':
		return "a zsh-only flag; split with `tr`, `cut` or `read` in a pipe instead"
	case '=':
		return "zsh-only word splitting; in POSIX an unquoted $name splits on its own"
	case '~':
		return "zsh-only globbing of a value; in POSIX an unquoted $name globs on its own"
	}

	return "a zsh-only array expansion; loop over the words in POSIX instead"
}

// scanZsh adds a finding for each zsh-only parameter expansion that is code.
// The bash parser refuses these forms, so each one is first replaced by a plain parameter name that carries its number.
// The parser then decides what is code: single-quoted text, escaped text and heredoc bodies are not, and comment text is (as in the reference).
// The bash forms `${#x}`, `${x:-y}` and `${x/a/b}` are not zsh-only.
func (s *scanner) scanZsh(text string) {
	marked, forms := markZsh(text)
	if len(forms) == 0 {
		return
	}

	for _, n := range zshCode(marked) {
		if n < len(forms) {
			s.findings = append(s.findings, forms[n])
		}
	}
}

// zshCode returns the numbers of the stand-in names that are code in the text. A line that does not parse is code, because the parser cannot tell.
func zshCode(marked string) []int {
	var found []int

	units, bad := ParseUnits(marked)
	for _, line := range bad {
		found = append(found, markerNumbers(line)...)
	}

	for _, u := range units {
		found = append(found, zshInUnit(u)...)
	}

	return found
}

// markZsh returns the text with each zsh-only form replaced by a stand-in name, and the finding for each form by number.
func markZsh(text string) (string, []model.Finding) {
	var out strings.Builder

	var forms []model.Finding

	last := 0

	for _, at := range zshForm.FindAllStringIndex(text, -1) {
		head := at[0] + len("${")
		out.WriteString(text[last:head] + markerName + strconv.Itoa(len(forms)) + "_")

		forms = append(forms, zshFinding(text[at[0]:], zshHint(text[head])))
		last = at[1]
	}

	out.WriteString(text[last:])

	return out.String(), forms
}

// zshInUnit returns the numbers of the stand-in names that are code in the unit: parameter expansions outside heredoc bodies, and comment text.
func zshInUnit(u Unit) []int {
	var found []int

	syntax.Walk(u.File, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.Comment:
			found = append(found, markerNumbers(n.Text)...)
		case *syntax.Redirect:
			syntax.Walk(n.Word, func(inner syntax.Node) bool { return visitMarker(inner, &found) })

			return false
		default:
			return visitMarker(node, &found)
		}

		return true
	})

	return found
}

// visitMarker records the number of a stand-in name when the node is a parameter expansion that holds one.
func visitMarker(node syntax.Node, found *[]int) bool {
	if exp, ok := node.(*syntax.ParamExp); ok {
		*found = append(*found, markerNumbers("${"+exp.Param.Value)...)
	}

	return true
}

// markerNumbers returns the numbers of the stand-in names in a text.
func markerNumbers(text string) []int {
	var out []int

	for _, m := range markerRef.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}

	return out
}

func zshFinding(text, hint string) model.Finding {
	return model.Finding{Kind: model.FindingZshExpansion, Text: formText(text) + " is " + hint}
}

// formText returns the expansion that starts the text, up to its closing brace or a fixed length.
func formText(text string) string {
	if end := strings.IndexByte(text, '}'); end >= 0 && end < maxFormText {
		return text[:end+1]
	}

	return text[:min(len(text), maxFormText)]
}
