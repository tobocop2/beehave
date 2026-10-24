package forge

import (
	"errors"
	"fmt"

	"github.com/tobocop2/obee/internal/model"
)

// CodeRefused is the exit code of a refusal: the forge cannot be read, so the caller must not go on.
const CodeRefused = 2

// Error is a forge read that must stop the caller. Code is the exit code the command line uses.
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

// refusal builds a refusal from base. An answer of the wrong shape adds the command that sent it.
func refusal(base string, cause error) *Error {
	message := base

	var bad *model.DecodeError
	if errors.As(cause, &bad) {
		message += ": " + bad.Error()
	}

	return &Error{Code: CodeRefused, Message: message, Err: cause}
}
