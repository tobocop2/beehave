package flow

import (
	"context"
	"errors"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/workflow"
)

// runRedactor returns the one redactor of an engine or a client. It holds the value of every variable of env whose name marks a secret.
// The redactor skips a value shorter than agent.MinSecretLen.
func runRedactor(env map[string]string) func(string) string {
	var secrets []string

	for name, value := range env {
		if workflow.SecretEnvName(name) {
			secrets = append(secrets, value)
		}
	}

	return agent.Redactor(secrets...)
}

// scrubbedError is a step error whose text has no secret. It keeps the cause for errors.Is and errors.As.
type scrubbedError struct {
	text  string
	cause error
}

func (s *scrubbedError) Error() string { return s.text }

func (s *scrubbedError) Unwrap() error { return s.cause }

// scrub returns err with the secrets removed from its text. DBOS stores the text of a step error in the database, so the redaction of the log does not cover it.
func (e *Engine) scrub(err error) error {
	if err == nil {
		return nil
	}

	text := e.redact(err.Error())
	if text == err.Error() {
		return err
	}

	return &scrubbedError{text: text, cause: err}
}

// runStep runs fn as the step name, with its error redacted, and records the failure of a step whose record the store refused.
func runStep[T any](e *Engine, ctx dbos.Context, name string, fn func(context.Context) (T, error), opts ...dbos.StepOption) (T, error) { //nolint:ireturn // the step returns the caller's own type
	var (
		ran bool
		own error
	)

	out, err := dbos.RunAsStep(ctx, func(c context.Context) (T, error) {
		out, fnErr := fn(c)
		ran, own = true, e.scrub(fnErr)

		return out, own
	}, append([]dbos.StepOption{dbos.WithStepName(name)}, opts...)...)
	if err != nil && ran && ctx.Err() == nil && !errors.Is(err, own) {
		e.recordRefusedStep(ctx, name, err)
	}

	return out, err
}

// recordRefusedStep records refused as the failure of the step name, in a step of the same name at the next step id.
func (e *Engine) recordRefusedStep(ctx dbos.Context, name string, refused error) {
	failure := e.scrub(refused)

	_, err := dbos.RunAsStep(ctx, func(context.Context) (struct{}, error) { return struct{}{}, failure }, dbos.WithStepName(name))
	if err != failure { //nolint:errorlint // a DBOS error matches any error of its code under errors.Is, and only the recorded failure itself is a success
		e.logger.Warn("cannot record the failure of a step whose record the store refused; beehave resume of this run can fail at this step", "step", name, "error", err)
	}
}
