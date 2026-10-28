package shell

import "mvdan.cc/sh/v3/syntax"

// Reader turns the words of a command into text, the way the scan reads them. A check outside this package reads commands through it. A new Reader knows no variable: a variable stays as its name text.
type Reader struct {
	s *scanner
}

// NewReader returns a Reader with no variable known.
func NewReader() *Reader {
	return &Reader{s: newScanner()}
}

// Words expands each argument of a call to the words that a shell would run. Quotes go, and a variable stays as its name text.
func (r *Reader) Words(u Unit, args []*syntax.Word) []string {
	return r.s.words(u, args)
}

// Literal expands one word to one string. A variable stays as its name text.
func (r *Reader) Literal(u Unit, word *syntax.Word) string {
	return r.s.literal(u, word)
}

// HeadOf drops the assignments, the wrappers and a leading shell that runs a script file. It returns the command and the dropped words.
func HeadOf(words []string) ([]string, []string) {
	return headOf(words)
}

// IsShell reports whether the command name is a shell that reads a script.
func IsShell(name string) bool {
	return isShell(name)
}

// Lone returns the variable name when the word is only `$name`, `${name}` or one of them in double quotes.
func Lone(word *syntax.Word) (string, bool) {
	return lone(word)
}
