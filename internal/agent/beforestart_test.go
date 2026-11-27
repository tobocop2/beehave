package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// countStarts makes the BeforeStart function of the spec count its calls and return note. It records whether the CLI had started at each call.
func countStarts(f *fixture, note string) (*int, *bool) {
	calls, started := 0, false
	f.spec.BeforeStart = func(context.Context) string {
		calls++
		_, err := os.Stat(filepath.Join(f.records, "args"))
		started = err == nil

		return note
	}

	return &calls, &started
}

func TestBeforeStart_RunsOnceBeforeTheCLIAndItsNoteIsInTheResult(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := boxed(t)
	calls, started := countStarts(f, "the Go module download failed")

	got := agent.Runner{Version: seen("2.1.330 (Claude Code)\n")}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)
	assert.Equal(t, 1, *calls)
	assert.False(t, *started, "the CLI had not started")
	assert.Contains(t, got.Note, "the Go module download failed")
}

func TestBeforeStart_ItsTimeDoesNotCountAgainstTheTotalLimit(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := boxed(t)
	f.spec.Timeout = time.Hour
	clock := &fakeClock{}

	stops, resets := -1, -1
	f.spec.BeforeStart = func(context.Context) string {
		clock.mu.Lock()
		defer clock.mu.Unlock()

		stops, resets = clock.stops, clock.resets

		return ""
	}

	got := agent.Runner{AfterFunc: clock.AfterFunc, Version: seen("2.1.330 (Claude Code)\n")}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)
	assert.Equal(t, time.Hour, clock.limit, "the only timer is the total timer")
	assert.Equal(t, 1, stops, "the total timer is stopped while the hook runs")
	assert.Zero(t, resets, "the total timer is not running while the hook runs")
	assert.Equal(t, 1, clock.resetCount(), "the total timer starts again with the whole limit after the hook")
}

func TestBeforeStart_IsNotCalledForARunThatTheRunnerRefuses(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	for name, refuse := range map[string]func(*fixture, *agent.Runner){
		"adapter with no sandbox": func(f *fixture, _ *agent.Runner) { f.spec.Adapter = noSandbox{f.spec.Adapter} },
		"windows":                 func(_ *fixture, r *agent.Runner) { r.OS = "windows" },
		"cli not installed":       func(f *fixture, _ *agent.Runner) { f.spec.Env["PATH"] = t.TempDir() },
		"cli too old":             func(_ *fixture, r *agent.Runner) { r.Version = seen("2.1.100 (Claude Code)") },
	} {
		f := boxed(t)
		calls, _ := countStarts(f, "")
		runner := agent.Runner{Version: seen("2.1.330 (Claude Code)\n")}
		refuse(f, &runner)

		got := runner.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, name)
		assert.Zero(t, *calls, "%s: no download for a refused run", name)
	}
}
