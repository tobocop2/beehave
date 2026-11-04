package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func TestParseHostProvider(t *testing.T) {
	t.Parallel()

	for _, name := range model.HostProviders() {
		got, err := model.ParseHostProvider(string(name))
		require.NoError(t, err)
		assert.Equal(t, name, got)
	}

	_, err := model.ParseHostProvider("nomad")
	require.ErrorIs(t, err, model.ErrUnknownHostProvider)
	require.ErrorContains(t, err, "use one of skypilot, ssh, slurm")
}

func TestJobState_Terminal(t *testing.T) {
	t.Parallel()

	for state, want := range map[model.JobState]bool{
		model.JobPending: false, model.JobRunning: false, model.JobUnknown: false, "": false,
		model.JobSucceeded: true, model.JobFailed: true, model.JobCanceled: true,
		model.JobDied: true, model.JobStale: true, model.JobTimedOut: true, model.JobLost: false,
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
