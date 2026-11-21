package flow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/workflow"
)

// backoffFactor doubles the wait before each retry of a step.
const backoffFactor = 2

// retriedRC is the error of a try whose program exited with a code that a retry may cure. The workflow library retries the step on it.
type retriedRC struct{ rc int }

func (r *retriedRC) Error() string { return fmt.Sprintf("the program exited with code %d", r.rc) }

// tries counts the tries of one retried step in this process. A crash during the retries starts the count again, as the workflow library does.
type tries struct {
	policy  workflow.RetryPolicy
	failure workflow.FailureMode
	n       int
}

// start counts a new try.
func (t *tries) start() { t.n++ }

// last reports whether this try is the last one the policy allows.
func (t *tries) last() bool { return t.n > t.policy.Retries }

// retry reports whether the exit code of this try asks for another try: a code other than zero and not a stop, with a try left.
func (t *tries) retry(rc int, stops []int) bool {
	return rc != 0 && !slices.Contains(stops, rc) && !t.last()
}

// returned reports whether the error of this try becomes the result of the call: failure is "return", the run is not canceled,
// and the library would not try again (the last try, or an error that is not retried).
func (t *tries) returned(ctx context.Context, err error, retryable func(error) bool) bool {
	canceled := ctx.Err() != nil || errors.Is(err, context.Canceled)

	return t.failure == workflow.FailureReturn && !canceled && (t.last() || !retryable(err))
}

// retryOptions are the step options of a retry policy: the step retries of the workflow library, with the wait doubling before each retry.
func (e *Engine) retryOptions(policy workflow.RetryPolicy) []dbos.StepOption {
	if policy.Retries == 0 {
		return nil
	}

	opts := []dbos.StepOption{dbos.WithStepMaxRetries(policy.Retries), dbos.WithStepBackoffFactor(backoffFactor), dbos.WithStepRetryPredicate(e.retryable)}
	if first, most, set := retryWaits(policy); set {
		opts = append(opts, dbos.WithStepBaseInterval(first), dbos.WithStepMaxInterval(most))
	}

	return opts
}

// retryWaits are the first and the longest wait of a policy with a backoff: the backoff, and workflow.MaxBackoff, where the doubling stops.
// set is false for a policy with no backoff, which keeps the waits of the workflow library.
func retryWaits(policy workflow.RetryPolicy) (first, most time.Duration, set bool) {
	if policy.Backoff <= 0 {
		return 0, 0, false
	}

	return policy.Backoff, workflow.MaxBackoff, true
}

// retryable reports whether a retry may cure the error of a try: an exit code that asks for one, or an error that is none of the known errors.
// A known error (a cancel, a lost host, a refusal of the forge) is kept as it is and never retried.
func (e *Engine) retryable(err error) bool {
	var rc *retriedRC
	if errors.As(err, &rc) {
		return true
	}

	return !slices.ContainsFunc(e.knownErrors(), func(known error) bool { return errors.Is(err, known) })
}

// failedOutputs are the outputs of a command whose step failed and whose failure is "return": rc -1 and the error text with no secret.
func (e *Engine) failedOutputs(err error, attempts int) map[string]any {
	out := commandOutputs(CommandOutput{RC: failedRC}, attempts)
	out[errorField] = e.redact(err.Error())

	return out
}

// failedRC is the exit code in the result of a command whose step failed.
const failedRC = -1

// errorField is the result field of a command that holds the error of a step that failed.
const errorField = "error"
