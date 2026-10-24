package model

import (
	"encoding/json"
	"errors"
	"fmt"
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
