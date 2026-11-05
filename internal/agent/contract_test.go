package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/model"
)

// editAdapter is a CLI that edits by default and has its own way to turn edits off.
type editAdapter struct{ agent.Adapter }

func (editAdapter) Profile() agent.Profile {
	return agent.Profile{
		Binary: "claude", Args: []string{"-p"}, ConfigVars: []string{"CLAUDE_CONFIG_DIR"},
		NoEditArgs: []string{"--tools", "read,bash"},
		NoEditEnv:  map[string]string{"AGENT_PERMISSION": `{"edit":"deny"}`},
	}
}

func TestRunner_AStepWithNoEditsStartsWithTheNoEditArgumentsAndVariables(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", model.AgentClaude, "")
	f.spec.Adapter = editAdapter{claude.New()}
	f.spec.NoEdits = true

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Equal(t, "-p --tools read,bash", record(t, f.records, "args"))
	assert.Contains(t, record(t, f.records, "env"), `AGENT_PERMISSION={"edit":"deny"}`)
}

func TestRunner_AStepThatMayEditStartsWithNoNoEditArgumentsAndNoNoEditVariables(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := newFixture(t, "claude", model.AgentClaude, "")
	f.spec.Adapter = editAdapter{claude.New()}

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Equal(t, "-p", record(t, f.records, "args"))
	assert.NotContains(t, record(t, f.records, "env"), "AGENT_PERMISSION")
}

func TestRunner_ALaunchCannotUndoTheNoEditVariables(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	launch := func(string) agent.Launch {
		return agent.Launch{Env: map[string]string{"AGENT_PERMISSION": `{"edit":"allow"}`}}
	}
	f := newFixture(t, "claude", model.AgentClaude, "")
	f.spec.Adapter = launchAdapter{Adapter: editAdapter{claude.New()}, launch: launch}
	f.spec.Guard = "beehave check command"
	f.spec.NoEdits = true

	agent.Runner{}.Run(context.Background(), f.spec)

	assert.Contains(t, record(t, f.records, "env"), `AGENT_PERMISSION={"edit":"deny"}`)
	assert.NotContains(t, record(t, f.records, "env"), `"edit":"allow"`)
}

// streamAdapter is a CLI with no cost flag that reports "spent <n>" dollars on a line, and shell calls on lines "tool <n>".
type streamAdapter struct {
	agent.Adapter
	streams bool
}

func (a streamAdapter) Profile() agent.Profile {
	p := a.Adapter.Profile()
	p.BudgetFlag, p.StreamsCost = "", a.streams

	return p
}

func (streamAdapter) Calls(line string) agent.Seen {
	if dollars, ok := strings.CutPrefix(line, "spent "); ok {
		amount, err := strconv.ParseFloat(dollars, 64)
		if err != nil {
			return agent.Seen{}
		}

		return agent.Seen{Spent: amount, Priced: true}
	}

	return agent.Seen{}
}

// bothAdapter has a cost flag of its own and also streams its cost.
type bothAdapter struct{ streamAdapter }

func (a bothAdapter) Profile() agent.Profile {
	p := claude.New().Profile()
	p.StreamsCost = true

	return p
}

// failingLog is a log that cannot be written.
type failingLog struct{}

func (failingLog) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestRunner_ACLIThatCrashesBeforeItReportsACostKeepsItsExitedOutcomeAndExitCode(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 2.5)
	setMode(t, f, "fail")

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Equal(t, 7, got.ExitCode)
	assert.NotContains(t, got.Note, "reported no cost")
}

func TestRunner_ACappedRunThatReportsAnExplicitZeroCostRunsNormally(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 2.5)
	setMode(t, f, "spendzero")

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.InDelta(t, 0, got.Streamed, 0)
	assert.Empty(t, got.Note)
}

func TestRunner_ABudgetRefusalForNoCostKeepsTheNoteOfTheEndOfTheRun(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 2.5)
	setMode(t, f, "partial")
	f.spec.Log = failingLog{}

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeBudgetCap, got.Outcome)
	assert.Contains(t, got.Note, os.ErrClosed.Error())
	assert.Contains(t, got.Note, "reported no cost")
}

func TestRunner_AProfileWithACostFlagAndStreamedCostIsCappedByTheFlagOnly(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 1.5)
	setMode(t, f, "spend2")
	f.spec.Adapter = bothAdapter{streamAdapter{Adapter: claude.New(), streams: true}}

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome, "the runner does not cap a second time: two dollars is past 1.5, and the CLI flag owns the cap")
	assert.Contains(t, record(t, f.records, "args"), "--max-budget-usd 1.5")
	assert.InDelta(t, 2, got.Streamed, 0)
}

// setMode changes what the fake CLI of the fixture does.
func setMode(t *testing.T, f *fixture, mode string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(f.records, "mode"), []byte(mode), 0o600))
}

// dedupeAdapter reports "spent <id> <n>" dollars once for each id: a second line with the same id reports none. It keeps state between calls.
type dedupeAdapter struct {
	streamAdapter
	seen map[string]bool
}

func (a dedupeAdapter) Calls(line string) agent.Seen {
	rest, ok := strings.CutPrefix(line, "spent ")
	if !ok {
		return agent.Seen{}
	}

	id, dollars, _ := strings.Cut(rest, " ")
	if a.seen[id] {
		return agent.Seen{}
	}

	a.seen[id] = true

	return a.streamAdapter.Calls("spent " + dollars)
}

func TestRunner_ACappedRunThatReportsNoCostEndsAsABudgetCapNeverAsZero(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 2.5)
	setMode(t, f, "tools3")

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeBudgetCap, got.Outcome)
	assert.Contains(t, got.Note, "reported no cost")
	assert.InDelta(t, 0, got.Streamed, 0)
}

func TestRunner_ARunThatReportsItsCostUnderTheCapEndsNormallyAndKeepsTheSum(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 2.5)
	setMode(t, f, "spend2")

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.InDelta(t, 2, got.Streamed, 0)
}

func TestRunner_ARunWithNoCapKeepsTheSumOfItsStreamedCost(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 0)
	setMode(t, f, "spend2")

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.InDelta(t, 2, got.Streamed, 0)
}

func TestRunner_ACapCrossedOnTheLastLineAfterTheProcessExitedEndsAsABudgetCap(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 2.5)
	setMode(t, f, "spendtail")

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeBudgetCap, got.Outcome)
	assert.InDelta(t, 3, got.Streamed, 0)
}

func TestRunner_ALineWithANegativeOrNaNCostCannotDefeatTheCap(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	for _, mode := range []string{"spendneg", "spendnan"} {
		f := streamFixture(t, true, 2.5)
		setMode(t, f, mode)

		got := agent.Runner{}.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeBudgetCap, got.Outcome, mode)
		assert.Equal(t, -1, got.ExitCode, mode)
	}
}

func TestRunner_AnAdapterThatKeepsStateIsAskedForTheDollarsOfALineOnce(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 0)
	setMode(t, f, "spendid")
	f.spec.Adapter = dedupeAdapter{streamAdapter: streamAdapter{Adapter: claude.New(), streams: true}, seen: map[string]bool{}}

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.InDelta(t, 2, got.Streamed, 0, "id a counts once, id b once")
}

func streamFixture(t *testing.T, streams bool, maxBudget float64) *fixture {
	t.Helper()

	f := newFixture(t, "claude", model.AgentClaude, "spend5")
	f.spec.Adapter = streamAdapter{Adapter: claude.New(), streams: streams}
	f.spec.MaxBudget = maxBudget

	return f
}

func TestRunner_ACLIThatStreamsItsCostIsStoppedWhenTheSumPassesTheCap(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 2.5)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeBudgetCap, got.Outcome)
	assert.Equal(t, -1, got.ExitCode)
	assert.NotContains(t, record(t, f.records, "args"), "--max-budget-usd", "the CLI has no budget flag to set")
	assert.NotContains(t, record(t, f.records, "args"), "2.5")
}

func TestRunner_ACLIThatStreamsItsCostRunsOnWhenTheSumOnlyReachesTheCap(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 5)
	f.spec.IdleTimeout = 0
	f.spec.Timeout = 2_000_000_000

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeTimedOut, got.Outcome, "five dollars is not past a cap of five, so only the timeout ends the run")
}

func TestRunner_ACLIThatStreamsItsCostIsNotStoppedWhenTheSpecSetsNoCap(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, true, 0)
	f.spec.Timeout = 2_000_000_000

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeTimedOut, got.Outcome)
}

func TestRunner_ACLIThatDoesNotStreamItsCostStillRefusesACostCap(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f := streamFixture(t, false, 2.5)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "no option to cap the cost, so the run does not start")
}
