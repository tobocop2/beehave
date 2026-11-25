package model

import (
	"strconv"
	"strings"
)

// The kinds of work item an id can name.
const (
	KindForge = "gh"
	KindBeads = "bd"
	// KindRound is the kind of the item that a round starts as: round/<base>. The base is a branch name and holds "/".
	KindRound = "round"
)

const (
	idSeparator = '-'
	underscore  = '_'
)

// ItemKind returns KindForge for a forge number, KindBeads for a beads id, KindRound for a round id and "" for none of them.
func ItemKind(item string) string {
	if IsDigits(item) {
		return KindForge
	}

	if kind, base, found := strings.Cut(item, "/"); found && kind == KindRound {
		if ValidBase(base) {
			return KindRound
		}

		return ""
	}

	prefix, rest, found := strings.Cut(item, string(idSeparator))
	if !found || !validPrefix(prefix) || rest == "" || strings.IndexFunc(rest, notIDRune) >= 0 {
		return ""
	}

	return KindBeads
}

// SameItem reports whether two item ids name one item: the same text, or two forge numbers of the same value, as "0412" and "412" name issue 412.
func SameItem(a, b string) bool {
	if a == b {
		return true
	}

	if ItemKind(a) != KindForge || ItemKind(b) != KindForge {
		return false
	}

	numberA, errA := strconv.Atoi(a)
	numberB, errB := strconv.Atoi(b)

	return errA == nil && errB == nil && numberA == numberB
}

// RoundBase returns the base branch of a round item and true. It returns an empty string and false for any other item.
func RoundBase(item string) (string, bool) {
	if ItemKind(item) != KindRound {
		return "", false
	}

	_, base, _ := strings.Cut(item, "/")

	return base, true
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

// ValidBase reports whether text is the name of a round branch: segments of letters, digits, "_", "." and "-" joined by "/".
// Each segment starts with a letter or a digit, so the name holds no "..", no leading or trailing "/" and no empty segment.
func ValidBase(text string) bool {
	if strings.Contains(text, "..") {
		return false
	}

	for segment := range strings.SplitSeq(text, "/") {
		if segment == "" || NotAlnum(rune(segment[0])) || strings.IndexFunc(segment[1:], notIDRune) >= 0 {
			return false
		}
	}

	return true
}
