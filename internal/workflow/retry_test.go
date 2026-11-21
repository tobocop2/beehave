package workflow_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

func TestRetry_ReadsTheRetriesAndTheBackoffOfACall(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, backoff string
		retries       int
		want          workflow.RetryPolicy
	}{
		{name: "no_retries", want: workflow.RetryPolicy{}},
		{name: "retries_with_no_backoff", retries: 3, want: workflow.RetryPolicy{Retries: 3}},
		{name: "retries_with_a_backoff", retries: 2, backoff: "2s", want: workflow.RetryPolicy{Retries: 2, Backoff: 2 * time.Second}},
		{name: "most_retries", retries: workflow.MaxRetries, want: workflow.RetryPolicy{Retries: workflow.MaxRetries}},
		{name: "the_longest_backoff", retries: 1, backoff: "10m", want: workflow.RetryPolicy{Retries: 1, Backoff: workflow.MaxBackoff}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := workflow.Retry(tc.retries, tc.backoff)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRetry_RefusesAPolicyThatTheRunCannotApply(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, backoff, want string
		retries             int
	}{
		{name: "negative_retries", retries: -1, want: "retries is -1. Use a whole number from 0 to 10"},
		{name: "too_many_retries", retries: workflow.MaxRetries + 1, want: "retries is 11. Use a whole number from 0 to 10"},
		{name: "backoff_with_no_retries", backoff: "1s", want: "backoff is set, but the call has no retries"},
		{name: "backoff_of_zero", retries: 1, backoff: "0s", want: `backoff is "0s", which must be above zero`},
		{name: "backoff_that_is_not_a_duration", retries: 1, backoff: "soon", want: `backoff is "soon", which is not a duration`},
		{name: "backoff_above_the_longest_wait", retries: 1, backoff: "10m1s", want: `backoff is "10m1s", which is above the longest wait of 10m0s`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := workflow.Retry(tc.retries, tc.backoff)

			require.ErrorContains(t, err, tc.want)
		})
	}
}
