package flow

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
)

// turningPreflight is the preflight of the engine over a forge fake that changes: from its call number turnAt on, the pull request is the
// one that over describes. The first call is the intake of the run, so turnAt 2 changes the pull request before the first dispatch of the review loop.
type turningPreflight struct {
	t      *testing.T
	world  *shipWorld
	head   string
	turnAt int
	over   map[string]any
	gone   bool

	mu     sync.Mutex
	calls  int
	asked  []preflight.Request
	pulled []string
}

func (p *turningPreflight) Preflight(ctx context.Context, project, item string, req preflight.Request) (preflight.Result, error) {
	p.mu.Lock()
	p.calls++
	p.asked = append(p.asked, req)

	if p.calls == p.turnAt {
		p.world.pull = preflightPull(p.t, p.head, p.over)
	}
	turned := p.turnAt > 0 && p.calls >= p.turnAt
	p.mu.Unlock()

	if turned && p.gone {
		return preflightOver{goneBranch{p.world}}.Preflight(ctx, project, item, req)
	}

	return preflightOver{p.world}.Preflight(ctx, project, item, req)
}

func (p *turningPreflight) ReadPull(ctx context.Context, project, item string) (model.PullFacts, error) {
	p.mu.Lock()
	world := p.world
	p.pulled = append(p.pulled, project+"/"+item)
	p.mu.Unlock()

	return preflightOver{world}.ReadPull(ctx, project, item)
}

func (p *turningPreflight) IssueAssignees(ctx context.Context, project, repo string, number int) ([]string, error) {
	return preflightOver{p.world}.IssueAssignees(ctx, project, repo, number)
}

// stopLoopRig is the example rig with a review that asks for changes on every pass and a forge that turns at the call turnAt.
func stopLoopRig(t *testing.T, turnAt int, over map[string]any, gone bool) (*agentRig, string, *turningPreflight) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	world := &shipWorld{pull: preflightPull(t, rig.head, nil)}
	turning := &turningPreflight{t: t, world: world, head: rig.head, turnAt: turnAt, over: over}

	rig.h.preflight = turning
	turning.gone = gone
	opensAsDraft(t, rig, world)

	return rig, file, turning
}

// opensAsDraft makes the forge fake open the pull request as the real forge does: a draft. The pull request that the preflight reads is a draft from the open on.
func opensAsDraft(t *testing.T, rig *agentRig, world *shipWorld) {
	t.Helper()

	rig.h.forge.onCreate = func(string) { world.pull = preflightPull(t, rig.head, map[string]any{"isDraft": true}) }
}

func TestExampleLoop_TheDraftThatTheRunOpenedIsNotAStopOfItsLoop(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	world := &shipWorld{pull: preflightPull(t, rig.head, nil)}
	spy := &turningPreflight{t: t, world: world, head: rig.head}
	rig.h.preflight = spy
	opensAsDraft(t, rig, world)

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Nil(t, result.Stopped, "the run stops at its own draft")
	assert.True(t, childOf(t, result).Approved, "the run reaches the first scoped review and the full review")
	assert.NotEmpty(t, rig.models())
	require.NotEmpty(t, spy.asked)
	assert.Len(t, spy.pulled, len(spy.asked), "each check is followed by a read of the claims, and the loop accepts the draft that pr_open returned")
}

// stopLoopBound is the time that a run of a stop test has. A loop that does not stop runs on, so the bound turns that into a failure of the test and no hang.
const stopLoopBound = 90 * time.Second

// runPipelineWithin is runPipeline that fails the test when the run does not end within bound.
func runPipelineWithin(t *testing.T, engine *Engine, file string, bound time.Duration) (ItemResult, error) {
	t.Helper()

	done := make(chan runOutcome, 1)

	go func() {
		result, err := runPipeline(engine, file)
		done <- runOutcome{result: result, err: err}
	}()

	select {
	case got := <-done:
		return got.result, got.err
	case <-time.After(bound):
		t.Fatalf("the run did not end within %s: the loop did not stop at the stop", bound)

		return ItemResult{}, nil
	}
}

func TestExampleLoop_AStopBeforeAnAgentOfTheReviewLoopStartsThatAgentNoMore(t *testing.T) {
	t.Parallel()

	moved := strings.Repeat("e", 40)
	label := []any{map[string]any{"name": "hold"}}

	for name, tc := range map[string]struct {
		turnAt int
		over   map[string]any
		gone   bool
		models []string
		step   string
		stop   codeOfStop
		reason string
	}{
		"merged while the implementer works":                   {turnAt: 2, over: map[string]any{"state": "MERGED"}, models: []string{"opus", "sonnet"}, step: "preflight:before-scoped-review-1", stop: preflight.Merged, reason: "already MERGED"},
		"merged while the first review runs":                   {turnAt: 3, over: map[string]any{"state": "MERGED"}, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.Merged, reason: "already MERGED"},
		"closed while the first review runs":                   {turnAt: 3, over: map[string]any{"state": "CLOSED"}, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.Closed, reason: "CLOSED"},
		"a push by the author while the review runs":           {turnAt: 3, over: map[string]any{shipHeadKey: moved}, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.HeadMoved, reason: moved},
		"the own draft and a moved head: the head is reported": {turnAt: 3, over: map[string]any{"isDraft": true, shipHeadKey: moved}, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.HeadMoved, reason: moved},
		"a hold on the own draft: the hold is reported":        {turnAt: 3, over: map[string]any{"isDraft": true, "labels": label}, models: []string{"opus", "sonnet", "opus"}, step: "claim_stop:before-fix-1", stop: stopHeld, reason: "hold"},
		"a merge and a hold: the merge is reported":            {turnAt: 3, over: map[string]any{"state": "MERGED", "labels": label}, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.Merged, reason: "already MERGED"},
		"a gone branch and a moved head: the branch":           {turnAt: 3, over: map[string]any{shipHeadKey: moved}, gone: true, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.BranchGone, reason: "no longer exists"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, file, _ := stopLoopRig(t, tc.turnAt, tc.over, tc.gone)

			result, err := runPipelineWithin(t, rig.h.engine(t), file, stopLoopBound)

			require.NoError(t, err, "a stop is a result and not an error")
			assert.Equal(t, tc.models, rig.models(), "no agent starts after the stop")
			assert.Empty(t, result.Merge)
			require.NotNil(t, result.Stopped)
			assert.Equal(t, tc.step, result.Stopped.Step)
			assert.Equal(t, tc.stop.Code(), result.Stopped.RC)
			assert.Contains(t, result.Stopped.Output, tc.reason)
		})
	}
}

func TestReviewFix_ALoopWithNoGuardAsksTheForgeNothing(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	require.NoError(t, os.WriteFile(file, []byte(libraryLoop(rig.head, "")), 0o600))

	spy := &turningPreflight{t: t, world: &shipWorld{pull: preflightPull(t, rig.head, nil)}, head: rig.head}
	rig.h.preflight = spy

	loopEngine := rig.h.engine(t)
	_, err := loopEngine.RunFile(testItem, testProject, file, modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "opus"}, rig.models(), "the scoped review and the full review of the one candidate")
	assert.Empty(t, spy.asked, "without the argument guard the loop runs no preflight call")
}

func TestExampleLoop_EveryDispatchOfTheLoopIsCheckedAgainstTheHeadOfTheWorktree(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	})
	world := &shipWorld{pull: preflightPull(t, rig.head, nil)}
	spy := &turningPreflight{t: t, world: world, head: rig.head}
	rig.h.preflight = spy

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved)
	require.Len(t, spy.asked, 5, "the intake, then the scoped review, the fix, the second scoped review and the full review")

	for _, req := range spy.asked[1:] {
		assert.Equal(t, rig.head, req.ExpectHead, "the head the gate ran on, read from the worktree")
		assert.Equal(t, "integration", req.ExpectBase)
	}

	assert.Len(t, spy.pulled, 5, "each of the five checks is followed by a read of the claims")
}

func TestExampleLoop_AStopBeforeTheDesignStepStartsNoDesignAgent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, alwaysChanges, noFindings, sameMechanism, `{"decision":"replace","reason":"unused"}`)
	world := &shipWorld{pull: preflightPull(t, rig.head, nil)}
	// Calls: the intake, three scoped reviews with two fixes between them (five calls), the full review of candidate 1, then the design step of candidate 2 at call 8.
	rig.h.preflight = &turningPreflight{t: t, world: world, head: rig.head, turnAt: 8, over: map[string]any{"state": "MERGED"}}
	rig.h.writer = newFakeWriter()

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err, "a stop is a result and not an error")
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus", "opus"}, rig.models(), "the design agent never starts")
	require.NotNil(t, result.Stopped)
	assert.Equal(t, "preflight:before-design-2", result.Stopped.Step)
	assert.Equal(t, preflight.Merged.Code(), result.Stopped.RC)
}

func TestExampleLoop_TheSettleAgentStartsAfterTheRulingWhileThePullRequestIsStillTheOwnDraft(t *testing.T) {
	t.Parallel()

	rig, file, spy := stopLoopRig(t, 0, nil, false)
	rig.setRound("fix_rounds", oneFixRound)
	done := pipelineInBackground(rig.h.engine(t), file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	outcome := receive(t, done)

	require.NoError(t, outcome.err)
	assert.Nil(t, outcome.result.Stopped, "the check before the settle agent accepts the draft that the run opened")
	assert.Equal(t, append([]string{"opus", "sonnet"}, append(capModels(2), "sonnet")...), rig.models(), "the settle agent starts")
	assert.Equal(t, "integration", spy.asked[len(spy.asked)-1].ExpectBase, "the last call is the check before the settle agent")
}

func TestExampleLoop_AStopDuringTheRulingOfTheOwnerStartsNoSettleAgent(t *testing.T) {
	t.Parallel()

	// One fix round gives two candidates. Calls: the intake; candidate 1 with three scoped reviews, two fixes and the full review (six calls);
	// candidate 2 with three fixes, three scoped reviews and the full review (seven calls); then the settle agent at call 15 after the ruling.
	rig, file, _ := stopLoopRig(t, 15, map[string]any{"state": "MERGED"}, false)
	rig.setRound("fix_rounds", oneFixRound)
	engine := rig.h.engine(t)
	done := pipelineInBackground(engine, file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	outcome := receive(t, done)

	require.NoError(t, outcome.err, "a stop is a result and not an error")
	assert.Equal(t, append([]string{"opus", "sonnet"}, capModels(2)...), rig.models(), "the settle agent never starts")
	require.NotNil(t, outcome.result.Stopped)
	assert.Equal(t, "preflight:before-settle", outcome.result.Stopped.Step)
	assert.Equal(t, preflight.Merged.Code(), outcome.result.Stopped.RC)
}
