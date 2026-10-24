package store

import (
	"fmt"
	"os"

	"github.com/tobocop2/obee/internal/model"
)

// Exit codes a store refusal carries to the command line.
const (
	CodeCloseFailed = 1
	CodeRefused     = 2
	CodeDuplicate   = 3
	CodeUnlinked    = 4
)

// Error is a store call that must stop the caller. Code is the exit code the command line uses.
type Error struct {
	Code    int
	Message string
	ItemID  string
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

func refuse(format string, args ...any) *Error {
	return &Error{Code: CodeRefused, Message: fmt.Sprintf(format, args...)}
}

// CreateRequest holds the arguments of Create. Kind, Parent and Assignee are optional.
type CreateRequest struct {
	Title          string
	BodyFile       string
	Severity       model.Severity
	Kind           string
	Parent         string
	Assignee       string
	AllowDuplicate bool
}

// Validate refuses a request without a title, a non-empty body file or a severity from P0 to P3.
func (r CreateRequest) Validate() error {
	if r.Title == "" {
		return refuse("REFUSE: create needs --title")
	}

	if !nonEmptyFile(r.BodyFile) {
		return refuse("REFUSE: create needs --body-file with the finding and its reproduction")
	}

	if _, err := model.ParseSeverity(string(r.Severity)); err != nil {
		return refuse("REFUSE: create needs --severity P0, P1, P2 or P3, got '%s'", r.Severity)
	}

	return nil
}

func nonEmptyFile(path string) bool {
	if path == "" {
		return false
	}

	info, err := os.Stat(path)

	return err == nil && info.Size() > 0
}
