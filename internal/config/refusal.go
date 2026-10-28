package config

import (
	"errors"
	"fmt"
)

// ErrRefused marks a config problem that the user fixes by editing a file or a variable.
var ErrRefused = errors.New("refused")

// refusal wraps the formatted message, so an %w cause stays reachable with errors.Is.
type refusal struct {
	err error
}

func (r refusal) Error() string { return r.err.Error() }

func (r refusal) Unwrap() error { return r.err }

func (r refusal) Is(target error) bool { return target == ErrRefused }

func refuse(format string, args ...any) error {
	return refusal{err: fmt.Errorf(format, args...)}
}
