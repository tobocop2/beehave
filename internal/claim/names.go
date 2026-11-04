// Package claim keeps the claim on a work item: which run works on it, held in a row of the project's tracker.
package claim

import (
	"fmt"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/model"
)

// Exit codes a claim refusal carries to the command line.
const (
	CodeRefused = 2
	CodeHeld    = 3
)

const (
	ownerPrefix      = "session-"
	secondsPerHour   = 3600
	secondsPerMinute = 60
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

func held(format string, args ...any) *Error {
	return &Error{Code: CodeHeld, Message: fmt.Sprintf(format, args...)}
}

// OwnerOf returns the holder name of an owner token: a run of letters, digits and underscores.
func OwnerOf(token string) (string, error) {
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return model.NotAlnum(r) && r != underscore }) >= 0 {
		return "", refuse("claim: '%s' is not an owner token (letters, digits, underscore)", token)
	}

	return ownerPrefix + token, nil
}

// ItemOf returns item when it is an item id.
func ItemOf(item string) (string, error) {
	if model.ItemKind(item) == "" {
		return "", refuse("claim: '%s' is not an item id", item)
	}

	return item, nil
}

// age shows a duration as hours and minutes, such as 2h05m.
func age(span time.Duration) string {
	whole := int(span.Seconds())

	return fmt.Sprintf("%dh%02dm", whole/secondsPerHour, whole%secondsPerHour/secondsPerMinute)
}
