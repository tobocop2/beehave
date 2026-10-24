// Package claim keeps the claim on a work item: which run works on it, held in a beads row.
package claim

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/tobocop2/obee/internal/model"
)

// Exit codes a claim refusal carries to the command line.
const (
	CodeRefused = 2
	CodeHeld    = 3
)

const (
	ownerPrefix      = "session-"
	kindForge        = "gh"
	kindBeads        = "bd"
	secondsPerHour   = 3600
	secondsPerMinute = 60
	maxStderrRunes   = 200
	maxLeaseDigits   = 12
	maxTitleRunes    = 70
	idSeparator      = '-'
	rowTitlePrefix   = "lock:"
	forgeTitlePrefix = rowTitlePrefix + kindForge + "-"
	beadsTitlePrefix = rowTitlePrefix + kindBeads + "-"
	leaseKey         = "lease="
	leaseUnreadable  = -1
	leaseNone        = 0
	underscore       = '_'
)

// Error is a claim call that must stop the caller. Code 3 means another owner holds the item.
type Error struct {
	Code    int
	Message string
	Err     error
}

func (e *Error) Error() string {
	return e.Message
}

// Unwrap returns the cause of the refusal, such as a JSON syntax error.
func (e *Error) Unwrap() error {
	return e.Err
}

func refuse(format string, args ...any) *Error {
	return &Error{Code: CodeRefused, Message: fmt.Sprintf(format, args...)}
}

func refuseBecause(cause error, format string, args ...any) *Error {
	return &Error{Code: CodeRefused, Message: fmt.Sprintf(format, args...), Err: cause}
}

func held(format string, args ...any) *Error {
	return &Error{Code: CodeHeld, Message: fmt.Sprintf(format, args...)}
}

// KindOf returns "gh" for a forge number, "bd" for a beads id and "" for neither.
func KindOf(item string) string {
	if model.IsDigits(item) {
		return kindForge
	}

	prefix, rest, found := strings.Cut(item, string(idSeparator))
	if !found || !validPrefix(prefix) || rest == "" || strings.IndexFunc(rest, notIDRune) >= 0 {
		return ""
	}

	return kindBeads
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

// OwnerOf returns the holder name of an owner token: a run of letters, digits and underscores.
func OwnerOf(token string) (string, error) {
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return notAlnum(r) && r != underscore }) >= 0 {
		return "", refuse("claim: '%s' is not an owner token (letters, digits, underscore)", token)
	}

	return ownerPrefix + token, nil
}

// ItemOf returns item when it is a forge number or a beads id.
func ItemOf(item string) (string, error) {
	if KindOf(item) == "" {
		return "", refuse("claim: '%s' is neither a forge number nor a beads id", item)
	}

	return item, nil
}

// Age shows seconds as hours and minutes, such as 2h05m.
func Age(seconds float64) string {
	whole := int(seconds)

	return fmt.Sprintf("%dh%02dm", whole/secondsPerHour, whole%secondsPerHour/secondsPerMinute)
}

// storedLease reads the lease in a claim row description: leaseNone without one, leaseUnreadable when it is not a whole number of 1 to 12 digits above zero.
func storedLease(text string) int64 {
	start := leaseStart(text)
	if start < 0 {
		return leaseNone
	}

	token := text[start+len(leaseKey):]
	if end := strings.IndexFunc(token, unicode.IsSpace); end >= 0 {
		token = token[:end]
	}

	seconds, err := strconv.ParseInt(token, 10, 64)
	if len(token) > maxLeaseDigits || !model.IsDigits(token) || err != nil || seconds == 0 {
		return leaseUnreadable
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
