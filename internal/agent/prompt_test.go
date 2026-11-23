package agent_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/exectest"
)

func promptLaunch(launch agent.Launch) func(string) agent.Launch {
	return func(string) agent.Launch { return launch }
}

func TestRunner_ALaunchWithAnUnknownPromptModeDoesNotStart(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f, records := launchFixture(t, launchAdapter{Adapter: claude.New(nil), launch: promptLaunch(agent.Launch{Prompt: agent.PromptMode(9)})})

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "unknown prompt mode 9")
	assert.NoFileExists(t, filepath.Join(records, "args"))
}

func TestRunner_ALaunchWithAPrefixThatIsNotInstalledDoesNotStartAndNeverCallsBeforeStart(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f, records := launchFixture(t, launchAdapter{Adapter: claude.New(nil), launch: promptLaunch(agent.Launch{Prefix: []string{"no-such-wrapper", "--x"}})})
	called := false
	f.spec.BeforeStart = func(context.Context) string {
		called = true

		return ""
	}

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "no-such-wrapper is not installed")
	assert.False(t, called)
	assert.NoFileExists(t, filepath.Join(records, "args"))
}

// fileScript keeps the prompt file of the run: its content, its path and its mode, and what came on stdin.
const fileScript = `#!/bin/sh
here=$(dirname "$0")
for last; do :; done
cat "$last" > "$here/promptfile"
echo "$last" > "$here/promptpath"
ls -l "$last" | cut -c1-10 > "$here/promptmode"
cat > "$here/stdin"
`

// wrapScript is a prefix that keeps its first two arguments and starts the rest of the line.
const wrapScript = `#!/bin/sh
here=$(dirname "$0")
echo "$1|$2" > "$here/wrapped"
shift 2
exec "$@"
`

func TestRunner_APromptInAFileIsAPrivateFileThatTheRunRemovesAndNothingIsOnStdin(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f, records := launchFixture(t, launchAdapter{Adapter: claude.New(nil), launch: promptLaunch(agent.Launch{Prompt: agent.PromptFile})})
	require.NoError(t, exectest.Write(filepath.Join(records, "claude"), fileScript))

	got := agent.Runner{}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)
	assert.Equal(t, "do the step", record(t, records, "promptfile"))
	assert.Equal(t, "-rw-------", record(t, records, "promptmode"))
	assert.Empty(t, record(t, records, "stdin"))
	assert.NoFileExists(t, record(t, records, "promptpath"))
}

func TestRunner_APrefixIsOneProgramWithOneArgumentForEachWordAndTheCLIFollowsIt(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f, records := launchFixture(t, launchAdapter{Adapter: claude.New(nil), launch: promptLaunch(agent.Launch{Prefix: []string{"wrapper", "one word", "two"}})})
	require.NoError(t, exectest.Write(filepath.Join(records, "wrapper"), wrapScript))

	got := agent.Runner{}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)
	assert.Equal(t, "one word|two", record(t, records, "wrapped"))
	assert.Contains(t, record(t, records, "args"), "-p --output-format")
}

func TestRunner_APromptInAnArgumentIsTheLastArgumentAfterTheArgumentsOfTheLaunch(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f, records := launchFixture(t, launchAdapter{Adapter: claude.New(nil), launch: promptLaunch(agent.Launch{Prompt: agent.PromptArgument, Args: []string{"--from-launch"}})})

	got := agent.Runner{}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)
	assert.Contains(t, record(t, records, "args"), "--from-launch do the step")
}
