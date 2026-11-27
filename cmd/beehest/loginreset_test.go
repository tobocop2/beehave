package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/model"
)

// resetConfig has runner a over the five-hour cap, with that window resetting at resetTime, and runner b under both caps.
func resetConfig() config.Config {
	return config.Config{
		Projects: map[string]config.Project{"p": {}},
		Runners:  []model.Runner{{Name: "a", Agent: "claude"}, {Name: "b", Agent: "claude"}},
		Pools:    claudePools(),
	}
}

var resetTime = time.Date(2026, 11, 20, 5, 0, 0, 0, time.UTC)

func resetRead(_ context.Context, runner model.Runner) model.Reading {
	if runner.Name == "a" {
		return claudeUsage(model.Window{Percent: 100, ResetsAt: resetTime}, model.Window{Percent: 10})
	}

	return claudeUsage(model.Window{Percent: 10}, model.Window{Percent: 10})
}

func TestLoginsReset_ReadsTheUsageOfThatLoginAndGivesItsEarliestReset(t *testing.T) {
	t.Parallel()

	got, known, err := loginsReading(resetConfig(), resetRead).Reset(t.Context(), "p", "a")

	require.NoError(t, err)
	assert.True(t, known)
	assert.Equal(t, resetTime, got)
}

func TestLoginsReset_ALoginWithRoomHasNoResetTime(t *testing.T) {
	t.Parallel()

	_, known, err := loginsReading(resetConfig(), resetRead).Reset(t.Context(), "p", "b")

	require.NoError(t, err)
	assert.False(t, known, "the usage read of a login under both caps dates no limit")
}

func TestLoginsReset_ALoginWhoseUsageCannotBeReadHasNoResetTime(t *testing.T) {
	t.Parallel()

	read := func(context.Context, model.Runner) model.Reading { return model.Unknown("no usage") }

	_, known, err := loginsReading(resetConfig(), read).Reset(t.Context(), "p", "a")

	require.NoError(t, err)
	assert.False(t, known)
}

func TestLoginsReset_ARunnerTheConfigLacksIsAnError(t *testing.T) {
	t.Parallel()

	_, _, err := loginsReading(resetConfig(), resetRead).Reset(t.Context(), "p", "zed")

	require.ErrorIs(t, err, errUnknownRunner)
}

func TestLoginsReset_AProjectTheConfigLacksIsAnError(t *testing.T) {
	t.Parallel()

	_, _, err := loginsReading(resetConfig(), resetRead).Reset(t.Context(), "nope", "a")

	require.ErrorIs(t, err, errUnknownProject)
}
