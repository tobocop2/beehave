package flow

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/workflow"
)

// runRedactor returns the one redactor of an engine or a client. It holds the value of every variable of env whose name marks a secret.
// The redactor skips a value shorter than agent.MinSecretLen.
func runRedactor(env map[string]string) func(string) string {
	var secrets []string

	for _, name := range secretNames(env) {
		secrets = append(secrets, env[name])
	}

	return agent.Redactor(secrets...)
}

// secretNames returns the names of the variables of env that mark a secret and hold a value, sorted.
func secretNames(env map[string]string) []string {
	var names []string

	for name, value := range env {
		if value != "" && workflow.SecretEnv(name, value) {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	return names
}

// warnShortSecrets logs one warning for each secret variable whose value is too short to redact. The warning names the variable and never the value.
func warnShortSecrets(logger *slog.Logger, env map[string]string) {
	for _, name := range secretNames(env) {
		if len(env[name]) < agent.MinSecretLen {
			logger.Warn("the value of a secret variable is shorter than the minimum, so beehave does not remove it from the output", "variable", name, "minimum_bytes", agent.MinSecretLen)
		}
	}
}

// redactedCommands runs the programs of command calls and removes the secrets of the run from what they print.
type redactedCommands struct {
	inner  commands
	redact func(string) string
}

func (c redactedCommands) Run(ctx context.Context, request CommandRequest) (CommandOutput, error) {
	out, err := c.inner.Run(ctx, request)
	out.Stdout, out.Stderr = c.redact(out.Stdout), c.redact(out.Stderr)

	return out, err
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

// stepKey is the run id and the step id of the step that ctx belongs to, joined by a colon. A step that runs again after a crash has the same key.
// A context that is outside a run has no key.
func stepKey(ctx context.Context) string {
	step, _ := ctx.(dbos.Context) //nolint:errcheck // a context that is no step reads as nil, and nil has no run
	run, runErr := dbos.GetWorkflowID(step)
	id, idErr := dbos.GetStepID(step)

	if runErr != nil || idErr != nil {
		return ""
	}

	return run + ":" + strconv.Itoa(id)
}

// withJobKey returns the context of a step with the job key. It stays a step context, so the readers of the run in the step body still find it.
func withJobKey(step dbos.Context, key string) context.Context {
	return dbos.From(step, host.WithJobKey(step, key))
}

// runStep runs fn as the step name, with its error redacted, and records the failure of a step whose record the store refused.
func runStep[T any](e *Engine, ctx dbos.Context, name string, fn func(context.Context) (T, error), opts ...dbos.StepOption) (T, error) { //nolint:ireturn // the step returns the caller's own type
	var (
		ran bool
		own error
	)

	out, err := dbos.RunAsStep(ctx, func(c context.Context) (T, error) {
		step, _ := c.(dbos.Context) //nolint:errcheck // the workflow library runs every step body with a DBOS context
		out, fnErr := fn(withJobKey(step, stepKey(step)))
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
