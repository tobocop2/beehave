package flow

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

// These tests run the example file itself: the order of work of the queue pass and the steps of a take-over.

// itemsOf returns the items of the agent requests, without repeats, in sorted order.
func itemsOf(rig *agentRig) []string {
	var items []string

	for _, request := range rig.h.agents.seen() {
		if !slices.Contains(items, request.Item) {
			items = append(items, request.Item)
		}
	}

	slices.Sort(items)

	return items
}

func TestExampleQueue_ThePassStartsWorkInFlightBeforeNewWorkAndThenBySeverity(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.opts.Env["QUEUE_SWEEP"] = "true"
	held := queueItem("10", model.SeverityP0)
	held.Hold = true
	rig.h.queue = &fakeQueue{
		items: []model.Item{
			queueItem("21", model.SeverityP2), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP2),
			held, queueItem("11", model.SeverityP0),
		},
		pulls: []model.PullRequest{queuePull(t, 8, "23"), queuePull(t, 9, "11", "hold")},
	}

	// Each child stops at its first agent step, so the test reads only which items started.
	rig.h.agents.err = errFake

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)
	assert.Nil(t, result.Stopped)

	// The queue has two free slots. 23 has a pull request, so it starts first. 22 is P1 with a higher id than the P2 item 21, so severity alone puts it next.
	// 10 (tracker hold) and 11 (hold label on its pull request) are P0 and would outrank both, so only the ready filter keeps them out.
	assert.Equal(t, []string{"22", "23"}, itemsOf(rig), "the item with an open pull request starts first, then the most severe new item, and a held item never starts")
}

func TestExampleQueue_AnItemThatAlreadyFailedIsNotStartedAgain(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.opts.Env["QUEUE_SWEEP"] = "true"
	rig.h.queue = &fakeQueue{items: []model.Item{queueItem("21", model.SeverityP1)}}
	rig.h.agents.err = errFake
	engine := rig.h.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)
	require.NoError(t, err)
	require.Equal(t, []string{"21"}, itemsOf(rig), "the first pass starts the item and its run fails")

	started := len(rig.h.agents.seen())
	_, err = engine.RunFile(testItem, testProject, file)
	require.NoError(t, err)

	assert.Len(t, rig.h.agents.seen(), started, "the item is in the state failed, so the second pass does not offer it")
}

func TestExampleQueue_APassOfAnEmptyQueueStartsNothing(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.opts.Env["QUEUE_SWEEP"] = "true"
	rig.h.queue = &fakeQueue{}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Empty(t, rig.h.agents.seen())
}

// takeRig is the example rig for a take-over of the pull request 7: no agent writes a verdict, and every command passes.
func takeRig(t *testing.T) (*agentRig, string) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.opts.Env["TAKE_PR"] = "7"
	rig.h.opts.Env["TAKE_AUTHOR"] = "alice"

	return rig, file
}

// answerTheGrace sends the answer of the author to the grace question, once the question names its people.
func answerTheGrace(t *testing.T, rig *agentRig, text string) {
	t.Helper()

	client := rig.h.client(t, testVersion)
	waitForTheOwner(t, rig.h)
	eventually(t, func() bool { return askedOf(t, client) != nil })
	require.NoError(t, client.AnswerAs(testItem, "alice", text))
}

// shortGrace sets the grace period of the example copy to a fraction of an hour that the wait reaches in a moment.
func shortGrace(t *testing.T, file string) {
	t.Helper()

	text, err := os.ReadFile(file) //nolint:gosec // the path is the example copy that this test made in its own folder
	require.NoError(t, err)

	changed := strings.Replace(string(text), `GRACE_HOURS = "1"`, `GRACE_HOURS = "0.00002"`, 1)
	require.NotEqual(t, string(text), changed)
	require.NoError(t, os.WriteFile(file, []byte(changed), 0o600)) //nolint:gosec // the path is the example copy that this test made in its own folder
}

func TestExampleTake_TheReviewComesFirstThenTheNoticeThenTheTakeAfterTheGraceAnswer(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)
	engine := rig.h.engine(t)
	done := runInBackground2(engine, file)

	waitForTheOwner(t, rig.h)

	// At the grace question the review agent and the notice have run, and the take has not.
	assert.Len(t, rig.h.agents.seen(), 1, "the review of the pull request is the only agent step")
	assert.Contains(t, rig.h.agents.seen()[0].Prompt, "belongs to somebody else")
	assert.Equal(t, []string{"--apply"}, flagsOf(rig.h.commands.requests), "only the notice has run")

	answerTheGrace(t, rig, "RELEASE")
	outcome := <-done
	require.NoError(t, outcome.err)

	assert.Equal(t, []string{"--apply", "--take"}, flagsOf(rig.h.commands.requests))
}

func TestExampleTake_KeepFromTheAuthorStopsTheTakeAndNoClaimIsTried(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)
	done := runInBackground2(rig.h.engine(t), file)

	answerTheGrace(t, rig, "KEEP")
	require.NoError(t, (<-done).err)

	assert.Equal(t, []string{"--apply"}, flagsOf(rig.h.commands.requests), "the notice ran and the take did not")
}

func TestExampleTake_NoAnswerInTheGraceTimeTakesTheItem(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)
	shortGrace(t, file)

	outcome := <-runInBackground2(rig.h.engine(t), file)

	require.NoError(t, outcome.err)
	assert.Equal(t, []string{"--apply", "--take"}, flagsOf(rig.h.commands.requests), "the clock ran out with no answer, so the take ran")
}

func TestExampleTake_ARefusalOfTheTakeForRepliesWithNoTokenHoldsTheItem(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)
	shortGrace(t, file)
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if slices.Contains(request.Run, "--take") {
			return CommandOutput{RC: 2, Stdout: "REFUSE: 2 replies landed and none used KEEP or RELEASE. Read the intent and decide."}, nil
		}

		return CommandOutput{}, nil
	}

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Contains(t, result.Stopped.Output, "none used KEEP or RELEASE")
	assert.NotContains(t, rig.h.commands.requests[1].Run, "--reply-read", "no step of the workflow says the replies were read")
}

func TestExampleTake_TheIdleAndGraceHoursAreOneAndTheItemIsPassedToTheTool(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)
	done := runInBackground2(rig.h.engine(t), file)

	answerTheGrace(t, rig, "RELEASE")
	require.NoError(t, (<-done).err)

	first := strings.Join(rig.h.commands.requests[0].Run, " ")
	assert.Equal(t, "remedy take 7 --me owner --idle 1 --grace 1 --item "+testItem+" --apply", first)
	assert.True(t, strings.HasSuffix(strings.Join(rig.h.commands.requests[1].Run, " "), "--take"), "the take is the last word of the call")
}

func TestExampleTake_ARefusedNoticeEndsTheItemBeforeTheGraceQuestion(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)
	rig.h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{RC: 2, Stdout: "REFUSE: not stale"}, nil
	}

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Len(t, rig.h.commands.requests, 1, "a refused notice means no take is tried")
}

// flagsOf returns the mode flag (--apply or --take) of each take command.
func flagsOf(requests []CommandRequest) []string {
	var flags []string

	for _, request := range requests {
		for _, word := range request.Run {
			if word == "--apply" || word == "--take" {
				flags = append(flags, word)
			}
		}
	}

	return flags
}
