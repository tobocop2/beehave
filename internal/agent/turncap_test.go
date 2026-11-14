package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/model"
)

// stepAdapter reads each output line "tool <n>" as a shell call with the id of the line. A CLI with noTurnFlag has no turn cap of its own.
type stepAdapter struct {
	agent.Adapter
	noTurnFlag bool
}

func (a stepAdapter) Profile() agent.Profile {
	p := a.Adapter.Profile()
	if a.noTurnFlag {
		p.TurnsFlag = ""
	}

	return p
}

func (stepAdapter) Calls(line string) agent.Seen {
	if strings.HasPrefix(line, "tool ") {
		return agent.Seen{Shell: []agent.Call{{ID: line, Text: "ls"}}}
	}

	return agent.Seen{}
}

func stepFixture(t *testing.T, mode string, noTurnFlag bool, maxTurns int) *fixture {
	t.Helper()

	f := newFixture(t, "claude", model.AgentClaude, mode)
	f.spec.Adapter = stepAdapter{Adapter: claude.New(nil), noTurnFlag: noTurnFlag}
	f.spec.MaxTurns = maxTurns

	return f
}

func TestRunner_ACLIWithNoTurnCapIsStoppedWhenItStartsMoreShellCallsThanTheCap(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := stepFixture(t, "tools5", true, 2)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeTurnCap, got.Outcome)
	assert.Equal(t, -1, got.ExitCode)
	assert.NotContains(t, record(t, f.records, "args"), "--max-turns", "the CLI has no turn flag to set")
}

func TestRunner_TheRunFolderIsGoneAfterARunThatTheTurnCapKilled(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	launch, folders := foldersOf(map[string]string{"conf/hooks.json": "x"})
	f := stepFixture(t, "tools5", true, 2)
	f.spec.Adapter = launchAdapter{Adapter: f.spec.Adapter, launch: launch}
	f.spec.Guard = "beehave check command"

	got := agent.Runner{}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeTurnCap, got.Outcome)
	require.Len(t, folders(), 1)
	assert.NoDirExists(t, folders()[0])
}

func TestRunner_ARunWithAsManyShellCallsAsTheCapIsNotStopped(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := stepFixture(t, "tools3", true, 3)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Contains(t, f.log.String(), "tool 3")
}

func TestRunner_ACallThatTheStreamNamesTwiceCountsOnce(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := stepFixture(t, "twice", true, 1)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
}

func TestRunner_ACLIWithATurnFlagKeepsItsCapAndTheRunnerCountsNothing(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := stepFixture(t, "tools3", false, 1)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome, "three calls past a cap of 1 do not stop a CLI that caps its own turns")
	assert.Contains(t, record(t, f.records, "args"), "--max-turns 1")
}

func TestRunner_ARunWithNoTurnCapIsNeverCounted(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := stepFixture(t, "tools3", true, 0)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
}
