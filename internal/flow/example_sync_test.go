package flow

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

// These tests run the sync step of the round: the round merges a moved trunk into the round branch before the queue audit of a pass.

// syncReviewMarker is a phrase of the prompt of the sync review, so the fake agents can tell that review from the others.
const syncReviewMarker = "What the review of a sync must do"

// syncResolveMarker is a phrase of the prompt of the agent that resolves a sync conflict.
const syncResolveMarker = "What the sync step must do"

// syncRoundBase is the round base of the sync tests. The trunk is main.
const syncRoundBase = "integration"

// syncBranch is the sync branch that the fake tool names.
const syncBranch = "sync/integration/abcd1234"

// syncScript is the fake of the engine steps round_sync and round_land: each check call takes the next line, and the last line repeats. land is the line and the exit code of the landing.
type syncScript struct {
	lines  []string
	land   CommandOutput
	calls  int
	called func(n int) // when set, runs after check call number n

	head   string // the head of the sync branch that a synced line names
	tip    string // the round base tip that a synced line names
	review string // the verdict of the sync review
	rc     int    // the exit code of each check call
	idle   bool   // the agent that resolves a conflict makes no commit, as one that fails before it merges
}

// syncedLine is the line of a clean sync that the fake tool prints.
func (s *syncScript) syncedLine() string {
	return "state=synced branch=" + syncBranch + " pr=7 head=" + s.head + " base_tip=" + s.tip
}

// isSync reports whether the request is a call of the sync tool, and whether it is the landing.
func isSync(request CommandRequest) (bool, bool) {
	switch request.Run[0] {
	case syncProgram:
		return true, false
	case landProgram:
		return true, true
	default:
		return false, false
	}
}

// syncGate is the gate of the round rig that records the base of each gate of the round itself and can make that gate red.
type syncGate struct {
	gate

	mu       sync.Mutex
	bases    []string
	pinBases []string
	red      bool
}

func (g *syncGate) Submit(ctx context.Context, project, item, host, base, pinBase, baseSHA string, slot model.Slot) (GateJob, error) {
	if item == testItem {
		g.mu.Lock()
		g.bases = append(g.bases, base)
		g.pinBases = append(g.pinBases, pinBase)
		g.mu.Unlock()
	}

	return g.gate.Submit(ctx, project, item, host, base, pinBase, baseSHA, slot)
}

// roundPinBases returns the pin base of each gate of the round itself, in order.
func (g *syncGate) roundPinBases() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Clone(g.pinBases)
}

func (g *syncGate) Run(ctx context.Context, project, item, host string, job GateJob) (GateResult, error) {
	result, err := g.gate.Run(ctx, project, item, host, job)
	if item == testItem && g.red {
		result.Passed = false
		result.Steps = []GateStep{{Name: "command", Status: failStatus}}
	}

	return result, err
}

// roundBases returns the base of each gate of the round itself, in order.
func (g *syncGate) roundBases() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Clone(g.bases)
}

// withSync makes the command fake of the rig answer the sync tool with the script, and run a checkout of the round for real in the folder of the round.
// The round base is syncRoundBase, so a test tells the round base from the trunk main.
func withSync(t *testing.T, rig *roundRig, script *syncScript) {
	t.Helper()

	rig.setRound("base", syncRoundBase)
	// The checks before review of each child compare its head with the round base. With no such ref the child fails there, before its turn, and ends on its own.
	rig.repo.must(t, "update-ref", "refs/remotes/origin/"+syncRoundBase, rig.kids.base)

	script.head = rig.heads["3"]
	script.tip = rig.kids.base
	if script.land == (CommandOutput{}) {
		script.land = CommandOutput{Stdout: "state=landed pr=7 head=" + script.head + "\n"}
	}

	if script.review == "" {
		script.review = "approve"
	}

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if call, land := isSync(request); call {
			return script.answer(land), nil
		}

		if len(request.Run) > 2 && request.Run[0] == "git" && request.Run[1] == "checkout" {
			return checkoutInRound(t, rig, request.Run[1:]), nil
		}

		return previous(request)
	}

	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		if strings.Contains(request.Prompt, syncReviewMarker) {
			require.NoError(t, os.WriteFile(request.OutputFile, []byte(`{"verdict":"`+script.review+`"}`), 0o600))
		}

		if strings.Contains(request.Prompt, syncResolveMarker) && !script.idle {
			dir, err := rig.h.trees.per(t.Context(), testItem)
			require.NoError(t, err)
			commitIn(t, dir, "sync-merge.txt")
		}

		return inner(request)
	}
}

// answer is the output of one call of the sync tool.
func (s *syncScript) answer(land bool) CommandOutput {
	if land {
		return s.land
	}

	line := s.lines[min(s.calls, len(s.lines)-1)]
	s.calls++

	if s.called != nil {
		s.called(s.calls)
	}

	if line == "synced" {
		line = s.syncedLine()
	}

	return CommandOutput{Stdout: line + "\n", RC: s.rc}
}

// checkoutInRound runs git with the words in the folder of the round, as the command step of the round does.
func checkoutInRound(t *testing.T, rig *roundRig, words []string) CommandOutput {
	t.Helper()

	dir, err := rig.h.trees.per(t.Context(), testItem)
	require.NoError(t, err)

	result := repoGit{binary: rig.repo.binary, dir: dir, home: rig.repo.home}.Run(t.Context(), words)

	return CommandOutput{Stdout: result.Stdout, RC: result.ExitCode}
}

// syncEngine is the engine of the round rig with the gate that records the gates of the round.
func syncEngine(t *testing.T, rig *roundRig) (*Engine, *syncGate) {
	t.Helper()

	var recorder *syncGate

	engine := rig.engineWith(t, func(deps *Deps) {
		recorder = &syncGate{gate: deps.Gate}
		deps.Gate = recorder
	})

	return engine, recorder
}

// runSync runs the round to its first wait, as run does, on the engine of syncEngine. red makes the gate of the round red.
func runSync(t *testing.T, rig *roundRig, red bool) (ItemResult, *syncGate, error) {
	t.Helper()

	engine, recorder := syncEngine(t, rig)
	recorder.red = red
	rig.last = engine

	result, err := rig.runOn(t, engine)

	return result, recorder, err
}

// at returns the place of the first step whose name starts with the prefix, or -1.
func at(names []string, prefix string) int {
	return slices.IndexFunc(names, func(name string) bool { return strings.HasPrefix(name, prefix) })
}

// syncLands returns the landing calls of the round, words joined by a space.
func syncLands(rig *roundRig) []string {
	return commandsNamed(rig, landProgram)
}

func TestRoundSync_AMovedTrunkLandsThroughTheSyncPullRequestWhoseGateRunsOnTheMergedHeadBeforeTheNextItemStarts(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	script := &syncScript{lines: []string{"synced", "state=current"}}
	withSync(t, rig, script)

	_, recorder, err := runSync(t, rig, false)
	require.NoError(t, err)

	names := stepNames(t, rig.last)
	order := []string{"round_sync:sync-1#", "gate:sync-gate-1#", "agent:sync-review-1#", "round_land:sync-land-1#", "integration_pull:round-integration-sync-1#", "agent:queue-audit-1#", "spawn:round-spawn-1#"}

	for i := 1; i < len(order); i++ {
		require.GreaterOrEqual(t, at(names, order[i-1]), 0, "the step %s ran: %v", order[i-1], names)
		assert.Less(t, at(names, order[i-1]), at(names, order[i]), "%s comes before %s", order[i-1], order[i])
	}

	assert.Equal(t, []string{"round_sync --base integration --trunk main"}, commandsNamed(rig, syncProgram)[:1], "the round base, then the trunk")
	assert.Equal(t, []string{"round_land --pr 7 --head " + script.head + " --base-tip " + script.tip}, syncLands(rig))
	require.NotEmpty(t, recorder.roundBases())
	assert.Empty(t, recorder.roundBases()[0], "the gate of the sync head runs the gate command only, with no base")
}

func TestRoundSync_TheSyncGateReadsThePinFileAtTheRoundBaseAndRunsNoCandidateCheck(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	script := &syncScript{lines: []string{"synced", "state=current"}}
	withSync(t, rig, script)

	_, recorder, err := runSync(t, rig, false)
	require.NoError(t, err)

	assert.Equal(t, []string{syncRoundBase}, recorder.roundPinBases()[:1], "the pin base of the sync gate is the round base, not the trunk")
	assert.Equal(t, []string{""}, recorder.roundBases()[:1], "the sync gate has no base, so no new-tests check")
}

func TestRoundSync_ASyncedLineLandsWhateverTheOrderOfItsFieldsAndKeepsAnEqualSignInAValue(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	script := &syncScript{}
	withSync(t, rig, script)
	script.lines = []string{"base_tip=" + script.tip + " head=" + script.head + " note pr=7 branch=sync/a=b state=synced", "state=current"}

	_, _, err := runSync(t, rig, false)
	require.NoError(t, err)

	assert.Equal(t, []string{"git fetch origin sync/a=b"}, commandsNamed(rig, "git fetch origin"))
	assert.Equal(t, []string{"round_land --pr 7 --head " + script.head + " --base-tip " + script.tip}, syncLands(rig))
}

func TestRoundSync_AConflictThatTheAgentDoesNotResolveStopsTheRoundInRoundWaitWithTheReason(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.setRound("wait_hours", json.Number("1"))
	rig.setRound("steps", nil)
	withSync(t, rig, &syncScript{lines: []string{"state=conflict branch=" + syncBranch + " source=main base_tip=abc"}})

	engine := waitOverEngine(t, rig)

	in, err := pinnedInput(testItem, testProject, rig.file, modeOff())
	require.NoError(t, err)

	in.Entry = "round"
	result, err := engine.run(in)

	require.NoError(t, err)
	assert.Nil(t, result.Stopped)

	var value struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}

	require.NoError(t, json.Unmarshal(result.Value, &value))
	assert.Equal(t, "waiting", value.State)
	assert.Equal(t, "sync-conflict", value.Reason)

	joined := strings.Join(stepNames(t, engine), " ")
	assert.Contains(t, joined, "agent:sync-resolve-1#", "one agent step resolves the conflict")
	assert.Contains(t, joined, "round_sync:sync-again-1#", "the round checks the trunk again after the agent")
	assert.Contains(t, joined, "wait:round-wait#")
	assert.NotContains(t, joined, "agent:queue-audit-1#", "no item builds on the stale base")
	assert.NotContains(t, joined, "item_list:round-queue-1-items#")
	assert.NotContains(t, joined, "gate:")

	resolve := slices.IndexFunc(rig.h.agents.requests, func(request AgentRequest) bool { return strings.Contains(request.Prompt, syncResolveMarker) })
	require.GreaterOrEqual(t, resolve, 0, "the resolve agent ran with the sync prompt")
	assert.Contains(t, rig.h.agents.requests[resolve].Prompt, syncBranch)
	assert.Contains(t, rig.h.agents.requests[resolve].Prompt, "origin/main", "the agent merges the source that the line names")
	assert.Equal(t, "opus", rig.h.agents.requests[resolve].Model, "the project file names no steps: sync_resolve runs on its default model")
}

func TestRoundSync_ATrunkMergeThatAddsNoTestLandsItsSyncBecauseTheSyncGateRunsNoNewTestsCheck(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	script := &syncScript{lines: []string{"synced", "state=current"}}
	withSync(t, rig, script)

	_, recorder, err := runSync(t, rig, false)
	require.NoError(t, err)

	assert.Equal(t, []string{""}, recorder.roundBases(), "one gate of the round, with no base: the gate command only, no new-tests check")
	assert.Len(t, syncLands(rig), 1, "the sync lands")
	require.Len(t, rig.questions, 1)
	assert.NotContains(t, rig.questions[0], "sync-")
}

func TestRoundSync_ARefusedSyncWaitsWithTheReasonAndAfterTheOwnerLandsItTheRoundFindsNoTrunkMoveAndGoesOn(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.answers = []string{"CONTINUE", "STOP"}
	withSync(t, rig, &syncScript{lines: []string{"state=refused reason=sync-refused", "state=current"}})

	result, _, err := runSync(t, rig, false)

	require.NoError(t, err)
	assert.Nil(t, result.Stopped, "a refusal is no failed step")
	require.Len(t, rig.questions, 2)
	assert.Contains(t, rig.questions[0], "sync-refused")
	assert.NotContains(t, rig.questions[1], "sync-")

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.NotContains(t, joined, "agent:queue-audit-1#", "the refused pass builds nothing")
	assert.Contains(t, joined, "round_sync:sync-2#", "the next pass checks the trunk again")
	assert.Contains(t, joined, "agent:queue-audit-2#", "the trunk is current, so the round goes on")
	assert.Empty(t, syncLands(rig))
}

func TestRoundSync_ARedGateOnTheSyncHeadStopsTheRoundWithTheReasonAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	withSync(t, rig, &syncScript{lines: []string{"synced"}})

	_, _, err := runSync(t, rig, true)

	require.NoError(t, err)
	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "sync-red")
	assert.Empty(t, syncLands(rig))

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.NotContains(t, joined, "agent:sync-review-1#", "a red gate gets no review")
	assert.NotContains(t, joined, "agent:queue-audit-1#")
}

func TestRoundSync_AReviewThatDoesNotApproveTheSyncStopsTheRoundWithTheReasonAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	withSync(t, rig, &syncScript{lines: []string{"synced"}, review: "changes"})

	_, _, err := runSync(t, rig, false)

	require.NoError(t, err)
	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "sync-review. The review did not approve the sync pull request. Its findings are in the reply of the step sync-review of the last pass: beehave status with the run id of the round lists the steps.")
	assert.Empty(t, syncLands(rig))

	steps := strings.Join(stepNames(t, rig.last), " ")
	assert.Contains(t, steps, "agent:sync-review-1#", "the step that the reason points to is in the steps of the run")
	assert.NotContains(t, steps, "agent:queue-audit-1#")
}

func TestRoundSync_ALandingOnAMovedBaseGoesOnAndTheNextPassSyncsAgain(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"stale", "head-moved"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t, "")
			rig.answers = []string{"CONTINUE", "STOP"}
			withSync(t, rig, &syncScript{lines: []string{"synced"}, land: CommandOutput{RC: 2, Stdout: "state=" + state + "\n"}})

			result, _, err := runSync(t, rig, false)

			require.NoError(t, err)
			assert.Nil(t, result.Stopped)

			joined := strings.Join(stepNames(t, rig.last), " ")
			assert.Contains(t, joined, "agent:queue-audit-1#", "the pass goes on")
			assert.Contains(t, joined, "round_land:sync-land-2#", "the next pass syncs and lands again")
		})
	}
}

func TestRoundSync_ALineOrAnExitCodeTheRoundDoesNotKnowStopsTheRun(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		script *syncScript
		want   string
	}{
		"unknown_state":         {&syncScript{lines: []string{"state=odd"}}, "the state \"odd\", which the round does not know"},
		"synced_with_no_head":   {&syncScript{lines: []string{"state=synced branch=" + syncBranch + " pr=7"}}, "the synced result of sync-1 has no head, base_tip"},
		"conflict_no_source":    {&syncScript{lines: []string{"state=conflict branch=" + syncBranch}}, "the conflict result of sync-1 has no source"},
		"head_not_in_the_clone": {&syncScript{lines: []string{"state=synced branch=" + syncBranch + " pr=7 head=0123456789abcdef0123456789abcdef01234567 base_tip=abc"}}, "the step sync-checkout-1 could not put the folder"},
	}

	for name, c := range cases {
		script, want := c.script, c.want
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t, "")
			withSync(t, rig, script)

			_, _, err := runSync(t, rig, false)

			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
			assert.Empty(t, rig.questions, "the run stops before any wait")
		})
	}
}

func TestRoundSync_AConflictWhoseBranchesTheRoundCannotFetchStopsTheRunBeforeTheAgent(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	withSync(t, rig, &syncScript{lines: []string{"state=conflict branch=" + syncBranch + " source=main base_tip=abc"}})

	fetched := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if strings.Join(request.Run, " ") == "git fetch origin "+syncBranch+" main" {
			return CommandOutput{RC: 1, Stderr: "no remote"}, nil
		}

		return fetched(request)
	}

	_, _, err := runSync(t, rig, false)

	require.ErrorContains(t, err, "the step sync-resolve-fetch-1 could not fetch "+syncBranch+" and main")
	assert.NotContains(t, strings.Join(stepNames(t, rig.last), " "), "agent:sync-resolve-1#", "no agent starts on a folder without both branches")
}

// A write of the sync that the forge refuses ends the item with the error of the forge as its lines. The run does not fail.
func TestRoundSync_AWriteThatTheForgeRefusesEndsTheItemWithTheError(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		script *syncScript
		want   string
	}{
		"landing_with_no_state": {&syncScript{lines: []string{"synced"}, land: CommandOutput{RC: 2, Stdout: "REFUSE: the forge refused the merge\n"}}, "REFUSE: the forge refused the merge"},
		"check_exit_one_empty":  {&syncScript{lines: []string{""}, rc: 1}, "the sync ended with the exit code 1"},
		"check_not_found":       {&syncScript{lines: []string{""}, rc: 127}, "the sync ended with the exit code 127"},
	}

	for name, c := range cases {
		script, want := c.script, c.want
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t, "")
			withSync(t, rig, script)

			result, _, err := runSync(t, rig, false)

			require.NoError(t, err)
			require.NotNil(t, result.Stopped)
			assert.Contains(t, result.Stopped.Output, want)
			assert.Empty(t, rig.questions, "the item ends before any wait")
		})
	}
}

func TestRoundSync_AForgeFailureOfTheCheckEndsTheItemWithTheLineOfTheTool(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if call, _ := isSync(request); call {
			return CommandOutput{RC: 2, Stdout: "REFUSE: the forge answered 502"}, nil
		}

		return previous(request)
	}

	result, err := rig.run(t)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Contains(t, result.Stopped.Output, "502")
}

// itemTerminal reports whether the latest run of the item has ended, or cannot be read.
func itemTerminal(engine *Engine, item string) bool {
	last, found, err := latestRun(viewOf(engine.ctx, engine.dir), item)

	return err != nil || (found && last.terminal())
}

// release lets the child of the item end now, in place of its turn at a queue read: a held pass reads no queue.
func (s *endOrder) release(item string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if mine := s.turns[item]; mine != nil {
		close(mine.open)
		delete(s.turns, item)
	}
}

// syncRequest returns the request of the first agent whose prompt holds the marker.
func syncRequest(t *testing.T, rig *roundRig, marker string) AgentRequest {
	t.Helper()

	at := slices.IndexFunc(rig.h.agents.requests, func(request AgentRequest) bool { return strings.Contains(request.Prompt, marker) })
	require.GreaterOrEqual(t, at, 0, "an agent ran with the prompt that holds %q", marker)

	return rig.h.agents.requests[at]
}

func TestRoundSync_TheResolveAgentMergesTheSourceThatTheLineNames(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"main", syncRoundBase} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t, "")
			withSync(t, rig, &syncScript{lines: []string{"state=conflict branch=" + syncBranch + " source=" + source + " base_tip=abc"}})

			_, _, err := runSync(t, rig, false)
			require.NoError(t, err)

			prompt := syncRequest(t, rig, syncResolveMarker).Prompt
			assert.Contains(t, prompt, "git merge --no-ff origin/"+source+"`", "the agent merges the source of the line, not a fixed branch")
			assert.Contains(t, prompt, "the round branch `"+syncRoundBase+"`")
		})
	}
}

func TestRoundSync_AConflictThatTheAgentResolvesIsGatedReviewedAndLandedInTheSamePass(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	script := &syncScript{lines: []string{"state=conflict branch=" + syncBranch + " source=main base_tip=abc", "synced", "state=current"}}
	withSync(t, rig, script)

	_, recorder, err := runSync(t, rig, false)
	require.NoError(t, err)

	names := stepNames(t, rig.last)
	order := []string{"round_sync:sync-1#", "command:sync-resolve-fetch-1#", "agent:sync-resolve-1#", "push:push-sync-1#", "round_sync:sync-again-1#", "gate:sync-gate-1#", "agent:sync-review-1#", "round_land:sync-land-1#", "agent:queue-audit-1#"}

	for i := 1; i < len(order); i++ {
		require.GreaterOrEqual(t, at(names, order[i]), 0, "the step %s ran: %v", order[i], names)
		assert.Less(t, at(names, order[i-1]), at(names, order[i]), "%s comes before %s", order[i-1], order[i])
	}

	require.Len(t, rig.h.forge.pushed, 1, "the engine pushes the head of the resolve once")
	assert.Contains(t, rig.h.forge.pushed[0], "|"+syncBranch+"|", "the push goes to the sync branch that the line names")
	assert.True(t, strings.HasSuffix(rig.h.forge.pushed[0], "|"), "the first push of the sync branch leases the head that the clone saw")

	assert.Equal(t, []string{"round_land --pr 7 --head " + script.head + " --base-tip " + script.tip}, syncLands(rig))
	assert.Equal(t, []string{""}, recorder.roundBases())
	require.Len(t, rig.questions, 1)
	assert.NotContains(t, rig.questions[0], "sync-")
}

func TestRoundSync_AResolveThatMadeNoCommitPushesNothingToTheSyncBranch(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	conflict := "state=conflict branch=" + syncBranch + " source=main base_tip=abc"
	withSync(t, rig, &syncScript{idle: true, lines: []string{conflict, conflict}})

	_, _, err := runSync(t, rig, false)
	require.NoError(t, err)

	assert.Empty(t, rig.h.forge.pushed, "a resolve that left the head where it started has nothing to push, and a push of that head would move the sync branch off its commits")
	assert.Empty(t, syncLands(rig))
}

func TestRoundSync_TheReviewOfTheSyncReadsOnlyAndRunsOnTheGatedHeadOnly(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.setRound("steps", nil)
	withSync(t, rig, &syncScript{lines: []string{"synced", "state=current"}})

	_, _, err := runSync(t, rig, false)
	require.NoError(t, err)

	review := syncRequest(t, rig, syncReviewMarker)
	assert.Equal(t, "opus", review.Model, "the project file names no steps: sync_review runs on its default model")
	assert.True(t, review.NoEdits, "the review cannot edit files")
	assert.Contains(t, review.Forbid, model.FindingGitWrite, "the review cannot write with git")

	moved := newRoundRig(t, "")
	withSync(t, moved, &syncScript{lines: []string{"synced"}})

	inner := moved.h.commands.reply
	moved.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 1 && request.Run[0] == "git" && request.Run[1] == "checkout" {
			return CommandOutput{}, nil
		}

		return inner(request)
	}

	_, _, err = runSync(t, moved, false)

	require.Error(t, err, "a folder that is not at the gated head gets no review")
	assert.Contains(t, err.Error(), "the pin is")
	assert.Empty(t, syncLands(moved))
}

func TestRoundSync_AHeldRoundGathersItsOpenRunAndCallsNoSyncInALaterPass(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 2")
	script := &syncScript{lines: []string{"state=current", "state=refused reason=sync-refused"}}
	script.called = func(n int) {
		if n == 2 {
			// A member that ends before the held pass leaves that pass no open run to gather: member 2 must wait at its turn until this release.
			waitUntil(func() bool { return rig.kids.turns.isParked("2") || itemTerminal(rig.last, "2") })
			assert.True(t, rig.kids.turns.isParked("2"), "member 2 waits at its turn when the held pass starts")
			rig.kids.turns.release("2")
		}
	}
	withSync(t, rig, script)

	_, _, err := runSync(t, rig, false)
	require.NoError(t, err)

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.Contains(t, joined, "round_sync:sync-2#", "the second pass checks the trunk and is held")
	assert.NotContains(t, joined, "agent:queue-audit-2#", "the held pass starts no item")
	assert.Contains(t, joined, "first_of:round-gather-2#", "the held pass still waits for the open run")
	assert.NotContains(t, joined, "round_sync:sync-3#", "a later pass of a held round calls no sync")
	assert.ElementsMatch(t, []string{"1", "2"}, rig.kids.started())
	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "sync-refused", "the round waits with the reason once no run is open")
}
