package store

import (
	"errors"

	"github.com/tobocop2/beehave/internal/model"
)

// Unreadable turns the failure of model.Decode into a store refusal that names the command. The cause of the refusal is the cause of the decode failure. A nil error stays nil, and any other error passes through.
func Unreadable(err error) error {
	var bad *model.DecodeError
	if errors.As(err, &bad) {
		return unreadable(bad.Command, bad.Cause)
	}

	return err
}

// DecodeRows reads a JSON list and refuses a list of limit rows or more, which may be cut off.
func DecodeRows[T any](command, text string, limit int, what string) ([]T, error) {
	rows, err := model.Decode[[]T](command, text)
	if err != nil {
		return nil, Unreadable(err)
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
