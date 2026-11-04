package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ErrNullAnswer is the cause when a command prints null where a value is due.
var ErrNullAnswer = errors.New("the answer is null")

const nullPayload = "null"

// DecodeError is an answer of a command that does not match the expected shape. Cause stays reachable with errors.Is.
type DecodeError struct {
	Command string
	Cause   error
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("cannot read the answer of `%s` (%v)", e.Command, e.Cause)
}

// Unwrap returns the cause, such as a JSON syntax error.
func (e *DecodeError) Unwrap() error {
	return e.Cause
}

// Decode reads one JSON payload from a command into T. A payload of another shape, a null payload or text after the value is a *DecodeError that names the command.
func Decode[T any](command, text string) (T, error) { //nolint:ireturn // the decoder returns the caller's own type
	var out T

	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return out, &DecodeError{Command: command, Cause: err}
	}

	if strings.TrimSpace(text) == nullPayload {
		return out, &DecodeError{Command: command, Cause: ErrNullAnswer}
	}

	return out, nil
}

var errNullInPages = errors.New("a page or an item is null")

// DecodePages reads the output of `gh api --paginate --slurp`, a list of pages, as one list. A null page or a null item is an answer of the wrong shape, not an empty one.
func DecodePages[T any](command, text string) ([]T, error) {
	pages, err := Decode[[]*[]*T](command, text)
	if err != nil {
		return nil, err
	}

	items := []T{}

	for _, page := range pages {
		if page == nil || slices.Contains(*page, nil) {
			return nil, &DecodeError{Command: command, Cause: errNullInPages}
		}

		for _, item := range *page {
			items = append(items, *item)
		}
	}

	return items, nil
}

// Deref returns the text, or empty for nil.
func Deref(text *string) string {
	if text == nil {
		return ""
	}

	return *text
}

// SplitRepo splits a repository written owner/name. Any other spelling is not ok.
func SplitRepo(repo string) (owner, name string, ok bool) {
	owner, name, found := strings.Cut(repo, "/")

	return owner, name, found && IsRepoPart(owner) && IsRepoPart(name)
}

// URLNumber returns the number at the end of an address such as https://github.com/o/r/<kind>/<number>, which gh prints after it creates a pull request or an issue.
func URLNumber(text, kind string) (int, bool) {
	text = strings.TrimSpace(text)
	cut := strings.LastIndex(text, "/")

	if cut < 0 || !strings.HasSuffix(text[:cut], "/"+kind) || !IsDigits(text[cut+1:]) {
		return 0, false
	}

	number, err := strconv.Atoi(text[cut+1:])

	if err != nil || number <= 0 {
		return 0, false
	}

	return number, true
}
