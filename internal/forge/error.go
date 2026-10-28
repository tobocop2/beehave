package forge

import (
	"errors"
	"fmt"

	"github.com/tobocop2/obee/internal/model"
)

// ErrNoForge is the cause when a project has no forge and a caller asks for one. A caller that must have a forge tests for it with errors.Is.
var ErrNoForge = errors.New("no forge")

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

// Refuse returns a refusal with the formatted message.
func Refuse(format string, args ...any) *Error {
	return &Error{Code: CodeRefused, Message: fmt.Sprintf(format, args...)}
}

// Refusal builds a refusal from base. An answer of the wrong shape adds the command that sent it.
func Refusal(base string, cause error) *Error {
	message := base

	var bad *model.DecodeError
	if errors.As(cause, &bad) {
		message += ": " + bad.Error()
	}

	return &Error{Code: CodeRefused, Message: message, Err: cause}
}
