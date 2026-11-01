package flow

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/preflight"
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

	mu    sync.Mutex
	calls int
	asked []preflight.Request
}

func (p *turningPreflight) Preflight(ctx context.Context, project, item string, req preflight.Request, hold string) (preflight.Result, error) {
	p.mu.Lock()
	p.calls++
	p.asked = append(p.asked, req)

	if p.calls == p.turnAt {
		p.world.pull = preflightPull(p.t, p.head, p.over)
	}
	turned := p.turnAt > 0 && p.calls >= p.turnAt
	p.mu.Unlock()

	if turned && p.gone {
		return preflightOver{goneBranch{p.world}}.Preflight(ctx, project, item, req, hold)
	}

	return preflightOver{p.world}.Preflight(ctx, project, item, req, hold)
}

// stopLoopRig is the example rig with a review that asks for changes on every pass and a forge that turns at the call turnAt.
func stopLoopRig(t *testing.T, turnAt int, over map[string]any, gone bool) (*agentRig, string, *turningPreflight) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	world := &shipWorld{pull: preflightPull(t, rig.head, nil)}
	turning := &turningPreflight{t: t, world: world, head: rig.head, turnAt: turnAt, over: over}

	rig.h.preflight = turning
	turning.gone = gone

	return rig, file, turning
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
		stop   preflight.Stop
		reason string
	}{
		"merged while the implementer works":              {turnAt: 2, over: map[string]any{"state": "MERGED"}, models: []string{"opus", "sonnet"}, step: "preflight:before-review-1", stop: preflight.Merged, reason: "already MERGED"},
		"merged while the first review runs":              {turnAt: 3, over: map[string]any{"state": "MERGED"}, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.Merged, reason: "already MERGED"},
		"closed while the first review runs":              {turnAt: 3, over: map[string]any{"state": "CLOSED"}, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.Closed, reason: "CLOSED"},
		"a push by the author while the review runs":      {turnAt: 3, over: map[string]any{shipHeadKey: moved}, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.HeadMoved, reason: moved},
		"a draft and a moved head: the draft is reported": {turnAt: 3, over: map[string]any{"isDraft": true, shipHeadKey: moved}, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.Draft, reason: "is a DRAFT"},
		"a hold and a draft: the hold is reported":        {turnAt: 3, over: map[string]any{"isDraft": true, "labels": label}, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.Held, reason: "hold"},
		"a merge and a hold: the merge is reported":       {turnAt: 3, over: map[string]any{"state": "MERGED", "labels": label}, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.Merged, reason: "already MERGED"},
		"a gone branch and a moved head: the branch":      {turnAt: 3, over: map[string]any{shipHeadKey: moved}, gone: true, models: []string{"opus", "sonnet", "opus"}, step: "preflight:before-fix-1", stop: preflight.BranchGone, reason: "no longer exists"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, file, _ := stopLoopRig(t, tc.turnAt, tc.over, tc.gone)

			result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

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
	require.NoError(t, os.WriteFile(file, []byte(libraryLoop("")), 0o600))

	spy := &turningPreflight{t: t, world: &shipWorld{pull: preflightPull(t, rig.head, nil)}, head: rig.head}
	rig.h.preflight = spy

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus"}, rig.models())
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

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
	require.Len(t, spy.asked, 4, "the intake, then the review, the fix and the second review")

	for _, req := range spy.asked[1:] {
		assert.Equal(t, rig.head, req.ExpectHead, "the head the gate ran on, read from the worktree")
		assert.Equal(t, "integration", req.ExpectBase)
		assert.Equal(t, []string{"owner"}, req.Own)
	}
}

func TestExampleLoop_AStopBeforeTheDesignStepStartsNoDesignAgent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, alwaysChanges, noFindings, sameMechanism, `{"decision":"replace","reason":"unused"}`)
	world := &shipWorld{pull: preflightPull(t, rig.head, nil)}
	// Calls: the intake, three reviews with two fixes between them (five calls), then the design step at call 7.
	rig.h.preflight = &turningPreflight{t: t, world: world, head: rig.head, turnAt: 7, over: map[string]any{"state": "MERGED"}}
	rig.h.writer = newFakeWriter()

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err, "a stop is a result and not an error")
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus"}, rig.models(), "the design agent never starts")
	require.NotNil(t, result.Stopped)
	assert.Equal(t, "preflight:before-design-3", result.Stopped.Step)
	assert.Equal(t, preflight.Merged.Code(), result.Stopped.RC)
}
