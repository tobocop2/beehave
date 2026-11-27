package flow

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/workflow"
)

// aLiveRunAsks is the fact that the latest run asks a question and has not been canceled. It names no run: an earlier run of the item has ended or is canceled.
func aLiveRunAsks(t *testing.T, h *harness) waiting[struct{}] {
	t.Helper()

	client := h.client(t, testVersion)

	return condition(func() bool {
		infos, err := client.Status()
		if err != nil {
			return false
		}

		return slices.ContainsFunc(infos, func(info RunInfo) bool { return info.Question != "" && info.State != runCancelled })
	})
}

// A run that was canceled after it spent the cap still counts: the count reads the run records whatever their state.
// The next run gets no candidate, not a new cap.
func TestLaneLayout_ACancelledFirstRunStillCountsAgainstTheCapOfThePullRequest(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	rig.setRound("fix_rounds", json.Number("2"))
	engine := rig.h.engine(t)
	client := rig.h.client(t, testVersion)

	first := pipelineInBackground(engine, file)
	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, client.Cancel(testItem))
	rig.h.tick(t)
	require.Error(t, receive(t, first).err)
	require.Equal(t, 3, countCandidates(rig), "the first run spends the cap of three candidates before the cancel")
	fixes := countFix(rig)

	second := pipelineInBackground(engine, file)
	waitOn(t, aLiveRunAsks(t, rig.h))
	require.NoError(t, client.Reply(testItem, Reply{Text: "park all"}))
	require.NoError(t, receive(t, second).err)

	assert.Equal(t, 3, countCandidates(rig), "the second run gets no candidate after a canceled first run spent the cap")
	assert.Equal(t, fixes, countFix(rig), "the second run starts no fix lane")
}

// A run that failed after it spent candidates still counts. An engine starts no new attempt after a failed one (the run continues with beehest resume),
// so the count is read from a script that is not part of that run.
func TestLaneLayout_AFailedRunStillCountsItsCandidates(t *testing.T) {
	t.Parallel()

	// Reviews 1 to 4 are candidate 1. Review 5 is the first scoped review of candidate 2, and review 6 writes no output.
	rig, file := exampleRig(t, func(review int) string {
		if review >= 6 {
			return ""
		}

		return "changes"
	})
	rig.setRound("fix_rounds", json.Number("3"))
	engine := rig.h.engine(t)

	_, err := runPipeline(engine, file)
	require.Error(t, err, "the sixth review writes no output, so the run fails")
	require.Equal(t, 4, countFix(rig), "two fixes of candidate 1 and two of candidate 2")

	infos, err := rig.h.client(t, testVersion).Status()
	require.NoError(t, err)
	require.Equal(t, runFailed, infos[0].State)

	assert.Equal(t, 2, spentCandidates(t, engine), "a failed run counts the two candidates it started")
}

// spentCandidates is the count of candidates that the runs of the item started, as the library reads it: the first scoped fixes and the scoped reviews of a clean implementer.
func spentCandidates(t *testing.T, engine *Engine) int {
	t.Helper()

	spent := 0

	for _, call := range []string{"fix-scoped-1", "scoped-review-implement"} {
		count, err := (&script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}).countEarlier(call)
		require.NoError(t, err)

		spent += count
	}

	return spent
}

// A candidate counts when its first scoped fix starts. A hold label that stops the run after that fix and before its scoped review still leaves the candidate spent.
func TestLaneLayout_ARunThatStopsBetweenAFixAndItsScopedReviewCountsTheCandidate(t *testing.T) {
	t.Parallel()

	// The reads of the pull request: the intake, three scoped reviews and two fixes of candidate 1, its full review, the first fix of candidate 2,
	// and the ninth before the scoped review of that fix. The hold label is on the pull request from the ninth read on.
	rig, file, turning := stopLoopRig(t, 9, map[string]any{"labels": []any{map[string]any{"name": "hold"}}}, false)
	rig.setRound("fix_rounds", json.Number("2"))
	engine := rig.h.engine(t)

	result, err := runPipeline(engine, file)
	require.NoError(t, err, "a stop is a result of the run")
	require.NotNil(t, result.Stopped)
	require.Equal(t, "claim_stop:before-scoped-review-2", result.Stopped.Step)
	require.Equal(t, 1, countCandidates(rig), "the run stops before the scoped review of candidate 2")
	require.Equal(t, 2, spentCandidates(t, engine), "candidate 1 and candidate 2, whose first fix started")

	// The owner takes the hold label off. The cap of three candidates has two spent, so the second run gets one candidate and then asks the owner.
	turning.mu.Lock()
	turning.world.pull = preflightPull(t, rig.head, nil)
	turning.mu.Unlock()

	second := pipelineInBackground(engine, file)
	waitOn(t, aSecondRunAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	require.NoError(t, receive(t, second).err)

	assert.Equal(t, 2, countCandidates(rig), "the second run gets the one candidate that is left")
}

// askFlow counts the earlier fix lanes, starts a fix lane, asks, and starts a late agent only when the count was zero.
const askFlow = `def main(item):
    n = earlier_calls(call = "fix", name = "spent").count
    agent(name = "fix", prompt = "prompts/work.md", model = "m")
    wait(name = "ask", question = "spent %d" % n)
    if n == 0:
        agent(name = "late", prompt = "prompts/work.md", model = "late-model")
`

// The count is a recorded step: a resume replays the count of the first start, even when another run of the item added fix lanes since.
func TestLaneLayout_AResumedRunKeepsTheCountOfEarlierFixLanesItStartedWith(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, askFlow)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	first := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, h))

	// Another run of the item (attempt 0, so the first run stays the latest) adds a fix lane while the first run waits.
	file := filepath.Join(filepath.Dir(engine.dir), defaultFlowName)
	pinned, err := workflow.Pin(file)
	require.NoError(t, err)

	_, err = engine.start(ItemInput{Item: testItem, Project: testProject, File: file, Files: pinned}, 0)
	require.NoError(t, err)
	waitOn(t, condition(func() bool { return len(h.agents.seen()) >= 2 }))

	count, err := (&script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}).countEarlier("fix")
	require.NoError(t, err)
	require.Equal(t, 2, count, "both runs started a fix lane, so a live read now gives two")

	require.NoError(t, client.Cancel(testItem))
	h.tick(t)
	require.Error(t, receive(t, first).err)

	require.NoError(t, client.Resume(testItem))
	waitOn(t, aLiveRunAsks(t, h))
	require.NoError(t, client.Reply(testItem, Reply{Text: "go"}))

	waitOn(t, waiting[struct{}]{what: "the late agent to start", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		return struct{}{}, slices.ContainsFunc(h.agents.seen(), func(r AgentRequest) bool { return r.Model == "late-model" }), nil
	}})
}

// aSecondRunAsks is the fact that a run of the item asks a question. The status lists the finished first run too, so it does not count the runs, and no run is named.
func aSecondRunAsks(t *testing.T, h *harness) waiting[struct{}] {
	t.Helper()

	client := h.client(t, testVersion)

	return condition(func() bool {
		infos, err := client.Status()
		if err != nil {
			return false
		}

		for _, info := range infos {
			if info.Question != "" {
				return true
			}
		}

		return false
	})
}

// The candidates of an item are counted over all its runs, because the run record of the engine outlives the run. A second run of the same item
// starts with the candidates the first run spent, so the cap is the cap of the pull request and not of one run. After the cap a run gets no candidate.
func TestLaneLayout_ASecondRunOfTheItemStartsNoFixLaneAfterTheFirstRunSpentTheCap(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	rig.setRound("fix_rounds", json.Number("2"))
	engine := rig.h.engine(t)

	first := pipelineInBackground(engine, file)
	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	require.NoError(t, receive(t, first).err)
	require.Equal(t, 3, countCandidates(rig), "the first run spends the cap of three candidates")
	fixes, gates := countFix(rig), rig.h.gate.calls

	second := pipelineInBackground(engine, file)
	waitOn(t, aSecondRunAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	require.NoError(t, receive(t, second).err)

	assert.Equal(t, fixes, countFix(rig), "the second run of the same item starts no fix lane: the pull request has used its two fix rounds")
	assert.Equal(t, 3, countCandidates(rig), "the second run gets no candidate and goes to the owner")
	assert.Equal(t, gates, rig.h.gate.calls, "the second run runs no gate")
}

// A store that is shut down gives no count: the call fails and never reads a missing count as zero, which would give the pull request a full cap again.
func TestLaneLayout_AStoreThatIsShutDownGivesNoCountOfEarlierCalls(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, err = (&script{e: engine, in: ItemInput{Item: testItem}}).countEarlier("fix")
	require.ErrorContains(t, err, "list the earlier runs of item 12")

	_, err = callsInRuns(engine.ctx, []string{"item-12-r1"}, "fix")
	require.ErrorContains(t, err, "read the steps of item-12-r1")
}

// A cap that the first run did not use up is what the second run gets: a first run that ended early after one candidate leaves one candidate fewer.
func TestLaneLayout_ASecondRunOfTheItemGetsTheCandidatesTheFirstRunLeft(t *testing.T) {
	t.Parallel()

	// Reviews 1 and 2 are the scoped review and the full review of candidate 1 of the first run, which approve; every later review asks for changes.
	rig, file := exampleRig(t, func(review int) string {
		if review <= 2 {
			return "approve"
		}

		return "changes"
	})
	rig.h.writer = newFakeWriter()
	engine := rig.h.engine(t)

	_, err := runPipeline(engine, file)
	require.NoError(t, err)
	require.Equal(t, 1, countCandidates(rig), "the first run approves its first candidate and ends")

	rig.setRound("fix_rounds", json.Number("2"))
	second := pipelineInBackground(engine, file)
	waitOn(t, aSecondRunAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	require.NoError(t, receive(t, second).err)

	assert.Equal(t, 3, countCandidates(rig), "the cap is three candidates for the pull request, one is spent, so the second run gets two")
}
