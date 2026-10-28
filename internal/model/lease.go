package model

import (
	"strconv"
	"strings"
	"unicode"
)

// The kinds of work item an id can name.
const (
	KindForge = "gh"
	KindBeads = "bd"
)

const (
	idSeparator    = '-'
	underscore     = '_'
	leaseKey       = "lease="
	maxLeaseDigits = 12
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
	if prefix == "" || strings.IndexFunc(prefix[:1], notAlnum) >= 0 {
		return false
	}

	return strings.IndexFunc(prefix, func(r rune) bool { return notAlnum(r) && r != underscore }) < 0
}

func notAlnum(r rune) bool {
	return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
}

func notIDRune(r rune) bool {
	return notAlnum(r) && r != underscore && r != '.' && r != idSeparator
}

// LeaseText returns the text a claim row stores for a lease of seconds.
func LeaseText(seconds int64) string {
	return leaseKey + strconv.FormatInt(seconds, 10)
}

// StoredLease reads the lease in a claim row description: LeaseNone without one, LeaseUnreadable when it is not a whole number of 1 to 12 digits above zero.
func StoredLease(text string) int64 {
	start := leaseStart(text)
	if start < 0 {
		return LeaseNone
	}

	token := text[start+len(leaseKey):]
	if end := strings.IndexFunc(token, unicode.IsSpace); end >= 0 {
		token = token[:end]
	}

	seconds, err := strconv.ParseInt(token, 10, 64)
	if len(token) > maxLeaseDigits || !IsDigits(token) || err != nil || seconds == 0 {
		return LeaseUnreadable
	}

	return seconds
}

// leaseStart finds the first "lease=" that does not follow a word character.
func leaseStart(text string) int {
	for offset := 0; ; {
		at := strings.Index(text[offset:], leaseKey)
		if at < 0 {
			return -1
		}

		at += offset
		if at == 0 || (notAlnum(rune(text[at-1])) && text[at-1] != underscore) {
			return at
		}

		offset = at + 1
	}
}
