package workflow

import (
	"errors"
	"fmt"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// FailureMode is what a command call does with an error of its step: raise it and end the run, or return it in the result.
type FailureMode string

// The values of the failure argument of a command call.
const (
	FailureRaise  FailureMode = "raise"
	FailureReturn FailureMode = "return"
)

// FailureModes lists the values of the failure argument of a command call.
func FailureModes() []string { return []string{string(FailureRaise), string(FailureReturn)} }

// MaxRetries is the most retries that one call takes.
const MaxRetries = 10

// The keywords of the retry policy of a call.
const (
	retriesKeyword = "retries"
	backoffKeyword = "backoff"
	failureKeyword = "failure"
	slotsKeyword   = "slots"
)

// errBackoffAlone refuses a backoff on a call with no retries.
var errBackoffAlone = errors.New("backoff is set, but the call has no retries. Set retries = 1 or more, or remove backoff")

// RetryPolicy is the number of retries of a call and the wait before its first retry. The wait doubles before each later retry.
// A zero Backoff leaves the wait to the workflow library.
type RetryPolicy struct {
	Retries int
	Backoff time.Duration
}

// Retry reads the retries and the backoff of a call. backoff is empty when the call names none, and it needs at least one retry.
func Retry(retries int, backoff string) (RetryPolicy, error) {
	if retries < 0 || retries > MaxRetries {
		return RetryPolicy{}, fmt.Errorf("retries is %d. Use a whole number from 0 to %d", retries, MaxRetries)
	}

	if backoff == "" {
		return RetryPolicy{Retries: retries}, nil
	}

	if retries == 0 {
		return RetryPolicy{}, errBackoffAlone
	}

	wait, err := backoffOf(backoff)

	return RetryPolicy{Retries: retries, Backoff: wait}, err
}

// backoffOf reads the backoff of a call: a duration above zero.
func backoffOf(text string) (time.Duration, error) {
	wait, err := Duration(backoffKeyword, text)
	if err == nil && wait <= 0 {
		err = fmt.Errorf("backoff is %q, which must be above zero. Write a duration such as \"2s\"", text)
	}

	return wait, err
}

// RetrySlots refuses retries on a command call that names a slot class: such a call runs as a job on the queue of its class, not as one step.
func RetrySlots(retries int, slots bool) error {
	if retries > 0 && slots {
		return errors.New("retries cannot be used with slots: a call with slots runs as a queued host job. Remove retries or slots")
	}

	return nil
}

// retriesRule is the rule of the retries keyword.
func retriesRule(v starlark.Value) error {
	retries, _ := starlark.AsInt32(v) //nolint:errcheck // the unpack check has refused a value that the destination cannot take
	_, err := Retry(retries, "")

	return err
}

// backoffRule is the rule of the backoff keyword.
func backoffRule(v starlark.Value) error {
	text, _ := starlark.AsString(v) // the unpack check has refused a value that is not text
	_, err := backoffOf(text)

	return err
}

// retrying checks the rules of a call that tie retries to backoff and to slots, as the run applies them. A retries that is not an int literal is left to the run.
func (g *graph) retrying(call *syntax.CallExpr, builtin string, params []Param) {
	retries, known := retriesLiteral(paramArg(call, params, retriesKeyword))
	if !known {
		return
	}

	if paramArg(call, params, backoffKeyword) != nil && retries == 0 {
		g.reportRule(call, fmt.Errorf("%s: %w", builtin, errBackoffAlone))
	}

	if builtin != commandName {
		return
	}

	if err := RetrySlots(retries, paramArg(call, params, slotsKeyword) != nil); err != nil {
		g.reportRule(call, fmt.Errorf("%s: %w", builtin, err))
	}
}

// retriesLiteral is the number of retries of a call: 0 when the call leaves retries out. known is false for a value that is not an int literal.
func retriesLiteral(arg syntax.Expr) (retries int, known bool) {
	if arg == nil {
		return 0, true
	}

	value, isLiteral := literalValue(arg)
	if !isLiteral {
		return 0, false
	}

	n, err := starlark.AsInt32(value)

	return n, err == nil
}
