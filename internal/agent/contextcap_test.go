package agent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/agent/claude"
)

// contextWindow is the window of the recorded stream: turn 2 holds 170012 tokens (85 percent), turn 3 holds 190005 (95 percent).
const contextWindow = 200000

// contextFixture plays the recorded stream of the Claude adapter through the runner. mode is "stream" (the CLI hangs after the stream) or "streamend" (it exits).
func contextFixture(t *testing.T, mode string, percent int, window int64) *fixture {
	t.Helper()

	recorded, err := os.ReadFile(filepath.Join("claude", "testdata", "context-stream.jsonl"))
	require.NoError(t, err)

	return contextTextFixture(t, mode, string(recorded), percent, window)
}

// contextTextFixture plays the given stream text through the runner with the context limit set.
func contextTextFixture(t *testing.T, mode, stream string, percent int, window int64) *fixture {
	t.Helper()

	f := newFixture(t, "claude", claude.Name, mode)
	require.NoError(t, os.WriteFile(filepath.Join(f.records, "stream"), []byte(stream), 0o600)) //nolint:gosec // the folder is a t.TempDir of the fixture

	f.spec.MaxContextPercent, f.spec.ContextWindow = percent, window

	return f
}

// oneTurnStream is a stream with one main-agent turn that holds the given number of tokens: 1000 of them cache reads, the rest input.
func oneTurnStream(tokens int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"only turn"}],"usage":{"input_tokens":%d,"cache_read_input_tokens":1000}},"parent_tool_use_id":null}`+"\n", tokens-1000)
}

func TestRunner_ARecordedStreamPastTheContextLimitEndsTheRunAsAContextCap(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := contextFixture(t, "stream", 80, contextWindow)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeContextCap, got.Outcome)
	assert.Equal(t, -1, got.ExitCode)
	assert.Contains(t, f.log.String(), "second turn", "the line that reaches the limit is in the log")
}

func TestRunner_ARecordedStreamUnderTheContextLimitRunsOn(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := contextFixture(t, "streamend", 96, contextWindow)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Contains(t, f.log.String(), "third turn")
}

func TestRunner_AContextFigureExactlyAtTheLimitStopsTheRun(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := contextTextFixture(t, "stream", oneTurnStream(100000), 50, contextWindow) // the only figure is 50 percent of the window

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeContextCap, got.Outcome)
}

func TestRunner_AContextFigureOneTokenUnderTheLimitDoesNotStopTheRun(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := contextTextFixture(t, "streamend", oneTurnStream(99999), 50, contextWindow)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Contains(t, f.log.String(), "only turn")
}

func TestRunner_ARunWithNoContextLimitOrNoWindowIsNeverStopped(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	for name, set := range map[string]struct {
		percent int
		window  int64
	}{"no percent": {0, contextWindow}, "no window": {1, 0}} {
		f := contextFixture(t, "streamend", set.percent, set.window)

		got := agent.Runner{}.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeExited, got.Outcome, name)
	}
}

func TestRunner_AnAdapterThatReportsNoContextFigureIsNeverStopped(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := stepFixture(t, "tools3", true, 0)
	f.spec.MaxContextPercent, f.spec.ContextWindow = 1, 1 // any figure would pass this limit

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Contains(t, f.log.String(), "tool 3")
}
