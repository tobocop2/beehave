package flow

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
)

// waitForTheQuestionOfALiveRun waits until the latest run asks a question and has not been canceled.
func waitForTheQuestionOfALiveRun(t *testing.T, h *harness) {
	t.Helper()

	client := h.client(t, testVersion)
	eventually(t, func() bool {
		infos, err := client.Status()
		if err != nil {
			return false
		}

		return slices.ContainsFunc(infos, func(info RunInfo) bool { return info.Question != "" && info.State != runCancelled })
	})
}

// A run that was canceled after it spent the cap still counts: the count reads the run records whatever their state.
func TestLaneLayout_ACancelledFirstRunStillCountsAgainstTheCapOfThePullRequest(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	rig.h.opts.Env["PATCH_ROUNDS"] = "2"
	engine := rig.h.engine(t)
	client := rig.h.client(t, testVersion)

	first := runInBackground2(engine, file)
	waitForTheOwner(t, rig.h)
	require.NoError(t, client.Cancel(testItem))
	rig.h.tick(t)
	require.Error(t, (<-first).err)
	require.Equal(t, 2, countFix(rig), "the first run spends the cap before the cancel")

	second := runInBackground2(engine, file)
	waitForTheQuestionOfALiveRun(t, rig.h)
	require.NoError(t, client.Answer(testItem, "park all"))
	require.NoError(t, (<-second).err)

	assert.Equal(t, 2, countFix(rig), "the second run starts no fix lane after a canceled first run spent the cap")
}

// A run that failed after it spent lanes still counts. An engine starts no new attempt after a failed one (the run continues with obee resume),
// so the count is read from a script that is not part of that run.
func TestLaneLayout_AFailedRunStillCountsItsFixLanes(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(review int) string {
		if review >= 3 {
			return ""
		}

		return "changes"
	})
	rig.h.opts.Env["PATCH_ROUNDS"] = "3"
	engine := rig.h.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)
	require.Error(t, err, "the third review writes no output, so the run fails")
	require.Equal(t, 2, countFix(rig))

	infos, err := rig.h.client(t, testVersion).Status()
	require.NoError(t, err)
	require.Equal(t, runFailed, infos[0].State)

	count, err := (&script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}).countEarlier("fix")
	require.NoError(t, err)
	assert.Equal(t, 2, count, "a failed run counts its two fix lanes")
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
	waitForTheOwner(t, h)

	// Another run of the item (attempt 0, so the first run stays the latest) adds a fix lane while the first run waits.
	file := filepath.Join(filepath.Dir(engine.dir), defaultFlowName)
	pinned, err := workflow.Pin(file)
	require.NoError(t, err)

	_, err = engine.start(ItemInput{Item: testItem, Project: testProject, File: file, Files: pinned}, 0)
	require.NoError(t, err)
	eventually(t, func() bool { return len(h.agents.seen()) >= 2 })

	count, err := (&script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}).countEarlier("fix")
	require.NoError(t, err)
	require.Equal(t, 2, count, "both runs started a fix lane, so a live read now gives two")

	require.NoError(t, client.Cancel(testItem))
	h.tick(t)
	require.Error(t, (<-first).err)

	require.NoError(t, client.Resume(testItem))
	waitForTheQuestionOfALiveRun(t, h)
	require.NoError(t, client.Answer(testItem, "go"))

	eventually(t, func() bool {
		return slices.ContainsFunc(h.agents.seen(), func(r AgentRequest) bool { return r.Model == "late-model" })
	})
}

// waitForTheOwnerOfASecondRun waits until a run of the item asks a question. The status lists the finished first run too, so it does not count the runs.
func waitForTheOwnerOfASecondRun(t *testing.T, h *harness) {
	t.Helper()

	client := h.client(t, testVersion)
	eventually(t, func() bool {
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

// The fix lanes of an item are counted over all its runs, because the run record of the engine outlives the run. A second run of the same item
// starts with the lanes the first run spent, so the cap is the cap of the pull request and not of one run.
func TestLaneLayout_ASecondRunOfTheItemStartsNoFixLaneAfterTheFirstRunSpentTheCap(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	rig.h.opts.Env["PATCH_ROUNDS"] = "2"
	engine := rig.h.engine(t)

	first := runInBackground2(engine, file)
	waitForTheOwner(t, rig.h)
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "park all"))
	require.NoError(t, (<-first).err)
	require.Equal(t, 2, countFix(rig), "the first run spends the cap")

	second := runInBackground2(engine, file)
	waitForTheOwnerOfASecondRun(t, rig.h)
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "park all"))
	require.NoError(t, (<-second).err)

	assert.Equal(t, 2, countFix(rig), "the second run of the same item starts no fix lane: the pull request has used its two")
}

// A store that is shut down gives no count: the call fails and never reads a missing count as zero, which would give the pull request a full cap again.
func TestLaneLayout_AStoreThatIsShutDownGivesNoCountOfEarlierFixLanes(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, err = (&script{e: engine, in: ItemInput{Item: testItem}}).countEarlier("fix")
	require.ErrorContains(t, err, "list the earlier runs of item 12")

	_, err = callsInRuns(engine.ctx, []string{"item-12-r1"}, "fix")
	require.ErrorContains(t, err, "read the steps of item-12-r1")
}

// A cap that the first run did not use up is what the second run gets.
func TestLaneLayout_ASecondRunOfTheItemGetsTheFixLanesTheFirstRunLeft(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(review int) string {
		if review == 2 {
			return "approve"
		}

		return "changes"
	})
	rig.h.writer = newFakeWriter()
	engine := rig.h.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)
	require.NoError(t, err)
	require.Equal(t, 1, countFix(rig))

	rig.h.opts.Env["PATCH_ROUNDS"] = "2"
	second := runInBackground2(engine, file)
	waitForTheOwnerOfASecondRun(t, rig.h)
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "park all"))
	require.NoError(t, (<-second).err)

	assert.Equal(t, 2, countFix(rig), "the cap is two for the pull request, one is spent, so the second run starts one more")
}
