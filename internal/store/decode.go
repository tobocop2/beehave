package store

import (
	"errors"

	"github.com/tobocop2/obee/internal/model"
)

// Decode reads one JSON payload from a command into T. A payload of another shape is a refusal that names the command.
func Decode[T any](command, text string) (T, error) { //nolint:ireturn // the decoder returns the caller's own type
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

// DecodeRows reads a JSON list and refuses a list of limit rows or more, which may be cut off.
func DecodeRows[T any](command, text string, limit int, what string) ([]T, error) {
	rows, err := Decode[[]T](command, text)
	if err != nil {
		return nil, err
	}

	if len(rows) >= limit {
		return nil, Refuse("store: %s returned %d rows, the list may be cut off; refusing", what, limit)
	}

	return rows, nil
}

// ReadableTitle returns the title, or a refusal when the answer has none.
func ReadableTitle(title *string) (string, error) {
	if title == nil {
		return "", Refuse("store: an item has no readable title; refusing")
	}

	return *title, nil
}
