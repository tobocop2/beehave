package forge

import (
	"errors"
	"fmt"

	"github.com/tobocop2/beehest/internal/model"
)

// ErrNoForge is the cause when a project has no forge and a caller asks for one. A caller that must have a forge tests for it with errors.Is.
var ErrNoForge = errors.New("no forge")

// ErrNoPull is the cause when the forge has no pull request on the branch of an item. An item that is not pushed yet is in this state, and it is not a failed read.
var ErrNoPull = errors.New("the item has no pull request")

// ErrWorkflowPermission is the cause of a write that the forge refuses because the token cannot change workflow files. A caller tests for it with errors.Is.
var ErrWorkflowPermission = errors.New("the token cannot change workflow files")

// ErrMergeConflict is the cause of a branch merge that the forge refuses because the two branches conflict. A caller tests for it with errors.Is.
var ErrMergeConflict = errors.New("the branches conflict")

// CodeRefused is the exit code of a refusal: the forge cannot be read, so the caller must not go on.
const CodeRefused = 2

// Error is a forge read that must stop the caller. Code is the exit code the command line uses.
// Detail is the message of the forge itself, or empty when the forge gave none. Err is the cause, such as ErrWorkflowPermission.
type Error struct {
	Code    int
	Message string
	Detail  string
	Err     error
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Message
	}

	return e.Message + ": " + e.Detail
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
