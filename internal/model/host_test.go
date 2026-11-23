package model_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestHostConfig_RunLimitSecondsRoundsAPartOfASecondUp(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		limit time.Duration
		want  int64
	}{
		"one second":                {limit: time.Second, want: 1},
		"exact seconds are kept":    {limit: 2 * time.Second, want: 2},
		"a day":                     {limit: 24 * time.Hour, want: 86400},
		"one nanosecond over":       {limit: time.Second + 1, want: 2},
		"1.0000001s":                {limit: 1000000100 * time.Nanosecond, want: 2},
		"1.9s":                      {limit: 1900 * time.Millisecond, want: 2},
		"the largest duration":      {limit: math.MaxInt64, want: 9223372037},
		"one second below the last": {limit: math.MaxInt64 - time.Second, want: 9223372036},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, model.HostConfig{MaxRun: tt.limit}.RunLimitSeconds())
		})
	}
}

func TestJobState_Terminal(t *testing.T) {
	t.Parallel()

	for state, want := range map[model.JobState]bool{
		model.JobPending: false, model.JobRunning: false, model.JobUnknown: false, "": false,
		model.JobSucceeded: true, model.JobFailed: true, model.JobCanceled: true,
		model.JobDied: true, model.JobStale: true, model.JobTimedOut: true, model.JobDiskFull: true, model.JobLost: false,
	} {
		assert.Equal(t, want, state.Terminal(), string(state))
	}
}

func TestRemoteHasHost(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"git@github.com:org/repo.git":   true,
		"github.com:org/repo.git":       true,
		"ssh://git@example.com/org/r":   true,
		"https://example.com/org/r.git": true,
		"file:///srv/repo.git":          false,
		"/srv/repo.git":                 false,
		"../repo.git":                   false,
		"./a:b":                         false,
		"":                              false,
		"https:///nohost":               false,
		"https://[bad":                  false,
		":path":                         false,
		"--upload-pack=x:y":             false,
		"-oProxyCommand=x@h:p":          false,
		"ext::sh -c id":                 false,
		"fd::17":                        false,
		"https://example.com/a::b":      false,
	}

	for remote, want := range cases {
		assert.Equal(t, want, model.RemoteHasHost(remote), remote)
	}
}
