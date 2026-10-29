package flow

import (
	"context"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// scrubbedError is a step error whose text has no secret. It keeps the cause for errors.Is and errors.As.
type scrubbedError struct {
	text  string
	cause error
}

func (s scrubbedError) Error() string { return s.text }

func (s scrubbedError) Unwrap() error { return s.cause }

// scrub returns err with the secrets removed from its text. DBOS stores the text of a step error in the database, so the redaction of the log does not cover it.
func (e *Engine) scrub(err error) error {
	if err == nil {
		return nil
	}

	text := e.redact(err.Error())
	if text == err.Error() {
		return err
	}

	return scrubbedError{text: text, cause: err}
}

// runStep runs fn as the step name. An error of the step passes through the redactor before it returns to DBOS.
func runStep[T any](e *Engine, ctx dbos.Context, name string, fn func(context.Context) (T, error), opts ...dbos.StepOption) (T, error) { //nolint:ireturn // the step returns the caller's own type
	return dbos.RunAsStep(ctx, func(c context.Context) (T, error) {
		out, err := fn(c)

		return out, e.scrub(err)
	}, append([]dbos.StepOption{dbos.WithStepName(name)}, opts...)...)
}
