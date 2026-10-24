package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

const (
	blank = ' '
	tab   = '\t'
)

// decode reads one JSON payload from a command into T. A payload of another shape is a refusal that names the command.
func decode[T any](command, text string) (T, error) { //nolint:ireturn // the decoder returns the caller's own type
	out, err := model.Decode[T](command, text)
	if err != nil {
		return out, refuseDecode(command, err)
	}

	return out, nil
}

// refuseDecode turns a decode failure into a store refusal. The cause of a *model.DecodeError is the refusal's cause; any other error is the cause itself.
func refuseDecode(command string, err error) *Error {
	var bad *model.DecodeError
	if errors.As(err, &bad) {
		return unreadable(command, bad.Cause)
	}

	return unreadable(command, err)
}

// decodeRows reads a JSON list and refuses a list of limit rows or more, which may be cut off.
func decodeRows[T any](command, text string, limit int, what string) ([]T, error) {
	rows, err := decode[[]T](command, text)
	if err != nil {
		return nil, err
	}

	if len(rows) >= limit {
		return nil, refuse("store: %s returned %d rows, the list may be cut off; refusing", what, limit)
	}

	return rows, nil
}

// normalTitle ignores case, runs of blanks and a blank at either end.
func normalTitle(title string) string {
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return r == blank || r == tab })

	return strings.Join(words, string(blank))
}

type openTitle struct {
	id    string
	title string
}

func refuseDuplicate(request CreateRequest, open []openTitle) error {
	if request.AllowDuplicate {
		return nil
	}

	wanted := normalTitle(request.Title)
	for _, other := range open {
		if normalTitle(other.title) == wanted {
			return &Error{
				Code:    CodeDuplicate,
				Message: fmt.Sprintf("REFUSED: %s is open with the same title. Comment there, or pass --allow-duplicate.", other.id),
			}
		}
	}

	return nil
}

func readableTitle(title *string) (string, error) {
	if title == nil {
		return "", refuse("store: an item has no readable title; refusing")
	}

	return *title, nil
}

func readableID(id string) (string, error) {
	if id == "" {
		return "", refuse("store: an item has no readable id; refusing")
	}

	return id, nil
}
