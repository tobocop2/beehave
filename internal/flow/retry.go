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

// firstWait and longestWait are the waits of a policy with no backoff: the wait before the first retry, and the wait where the doubling stops.
const (
	firstWait   = 100 * time.Millisecond
	longestWait = 5 * time.Second
)

// tryMark starts the name of the step of one try of a command call with retries: `<call>/try<n>`.
const tryMark = "/try"

// retriedRC is the error of a try whose program exited with a code that a retry may cure.
type retriedRC struct{ rc int }

func (r *retriedRC) Error() string { return fmt.Sprintf("the program exited with code %d", r.rc) }

// tries is the retry policy and the failure mode of one call. Each method takes n, the number of the try, counted from 1.
type tries struct {
	policy  workflow.RetryPolicy
	failure workflow.FailureMode
}

// last reports whether try n is the last one the policy allows.
func (t tries) last(n int) bool { return n > t.policy.Retries }

// retry reports whether the exit code of try n asks for another try: a code other than zero and not a stop, with a try left.
func (t tries) retry(n, rc int, stops []int) bool {
	return rc != 0 && !slices.Contains(stops, rc) && !t.last(n)
}

// again reports whether the error of try n asks for another try: the run is not canceled, a retry may cure the error, and a try is left.
func (t tries) again(ctx context.Context, n int, err error, retryable func(error) bool) bool {
	return !canceled(ctx, err) && retryable(err) && !t.last(n)
}

// returned reports whether the error of try n becomes the result of the call: failure is "return", the run is not canceled,
// and no other try comes (the last try, or an error that is not retried).
func (t tries) returned(ctx context.Context, n int, err error, retryable func(error) bool) bool {
	return t.failure == workflow.FailureReturn && !canceled(ctx, err) && (t.last(n) || !retryable(err))
}

// canceled reports whether the context of the try ended or the error is a cancel.
func canceled(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled)
}

// retryOptions are the step options of a retry policy: the step retries of the workflow library, with the wait doubling before each retry.
func (e *Engine) retryOptions(policy workflow.RetryPolicy) []dbos.StepOption {
	if policy.Retries == 0 {
		return nil
	}

	first, most := retryWaits(policy)

	return []dbos.StepOption{
		dbos.WithStepMaxRetries(policy.Retries), dbos.WithStepBackoffFactor(backoffFactor), dbos.WithStepRetryPredicate(e.retryable),
		dbos.WithStepBaseInterval(first), dbos.WithStepMaxInterval(most),
	}
}

// retryWaits are the first and the longest wait of a policy: the backoff and workflow.MaxBackoff, or firstWait and longestWait for a policy with no backoff.
func retryWaits(policy workflow.RetryPolicy) (first, most time.Duration) {
	if policy.Backoff <= 0 {
		return firstWait, longestWait
	}

	return policy.Backoff, workflow.MaxBackoff
}

// retryWait is the wait after try n: the first wait, doubled for each later try, and never above the longest wait.
func retryWait(policy workflow.RetryPolicy, n int) time.Duration {
	first, most := retryWaits(policy)

	return min(first<<(n-1), most)
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
