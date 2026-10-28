package store

import "fmt"

// CodeRefused is the exit code that a store refusal carries to the command line.
const CodeRefused = 2

// Error is a store call that must stop the caller. Code is the exit code the command line uses.
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

// unreadable refuses an answer of a command that does not match the expected shape. The cause stays reachable with errors.Is.
func unreadable(command string, cause error) *Error {
	return &Error{
		Code:    CodeRefused,
		Message: fmt.Sprintf("store: cannot read the answer of `%s` (%v); refusing", command, cause),
		Err:     cause,
	}
}

// Refuse returns a refusal with code 2 and the formatted message.
func Refuse(format string, args ...any) *Error {
	return &Error{Code: CodeRefused, Message: fmt.Sprintf(format, args...)}
}
