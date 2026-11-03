package model

import "strings"

// The kinds of work item an id can name.
const (
	KindForge = "gh"
	KindBeads = "bd"
)

const (
	idSeparator = '-'
	underscore  = '_'
)

// ItemKind returns KindForge for a forge number, KindBeads for a beads id and "" for neither.
func ItemKind(item string) string {
	if IsDigits(item) {
		return KindForge
	}

	prefix, rest, found := strings.Cut(item, string(idSeparator))
	if !found || !validPrefix(prefix) || rest == "" || strings.IndexFunc(rest, notIDRune) >= 0 {
		return ""
	}

	return KindBeads
}

func validPrefix(prefix string) bool {
	if prefix == "" || strings.IndexFunc(prefix[:1], NotAlnum) >= 0 {
		return false
	}

	return strings.IndexFunc(prefix, func(r rune) bool { return NotAlnum(r) && r != underscore }) < 0
}

// NotAlnum reports whether r is not an ASCII letter or digit.
func NotAlnum(r rune) bool {
	return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
}

func notIDRune(r rune) bool {
	return NotAlnum(r) && r != underscore && r != '.' && r != idSeparator
}
