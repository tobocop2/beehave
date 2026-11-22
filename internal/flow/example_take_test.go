package flow

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests call take_over of the example file directly. The file has no entry that calls it yet, so a test adds the entry `take` to its own copy.

// takeEntry is the entry that a take test adds to its copy of the example: the take-over of the pull request 7 of the author alice.
const takeEntry = `

def take(item):
    take_over(item, "7", "alice", facts(), settings(facts()))
`

// runTake runs the entry take of the file in the background, as a child run.
func runTake(engine *Engine, file string) <-chan runOutcome {
	done := make(chan runOutcome, 1)

	go func() {
		in, err := pinnedInput(testItem, testProject, file, modeOff())
		if err != nil {
			done <- runOutcome{err: err}

			return
		}

		in.Entry = "take"
		result, err := engine.run(in)
		done <- runOutcome{result: result, err: err}
	}()

	return done
}

// takeRig is the example rig for a take-over of the pull request 7: no agent writes a verdict, and every command passes.
func takeRig(t *testing.T) (*agentRig, string) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	text, err := os.ReadFile(file) //nolint:gosec // the path is the example copy that this test made in its own folder
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, append(text, takeEntry...), 0o600)) //nolint:gosec // the example copy of this test

	return rig, file
}

func TestExampleTake_TheCopyStartsWithTheExampleFile(t *testing.T) {
	t.Parallel()

	_, file := takeRig(t)

	copied, err := os.ReadFile(file) //nolint:gosec // the path is the example copy that this test made in its own folder
	require.NoError(t, err)

	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "remediation", "workflow.star"))
	require.NoError(t, err)

	assert.Equal(t, string(example)+takeEntry, string(copied), "the copy is the example file and the take entry after it, and nothing else")
}

// answerTheGrace sends the answer of the author to the grace question, once the question names its people.
func answerTheGrace(t *testing.T, rig *agentRig, text string) {
	t.Helper()

	client := rig.h.client(t, testVersion)
	waitOn(t, ownerAsks(t, rig.h))
	waitOn(t, waiting[struct{}]{what: "the question to name its people", run: runOfClient(t, client), read: func() (struct{}, bool, error) { return struct{}{}, askedOf(t, client) != nil, nil }})
	require.NoError(t, client.Reply(testItem, Reply{From: "alice", Text: text}))
}

// graceOverEngine launches an engine whose clock moves one hour at each reading, so the wait of the grace period of the example (one hour) is over at its second reading.
// The example file stays as it is.
func graceOverEngine(t *testing.T, h *harness) *Engine {
	t.Helper()

	start := time.Now()

	var readings atomic.Int64

	return launchedWith(t, h, func(deps *Deps) {
		deps.Clock.Reading = func() time.Time { return start.Add(time.Duration(readings.Add(1)) * time.Hour) }
		deps.Clock.OwnerWait = 10 * time.Millisecond
	})
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

func TestExampleTake_TheReviewComesFirstThenTheNoticeThenTheTakeAfterTheGraceAnswer(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)
	rig.setRound("steps", nil)
	done := runTake(rig.h.engine(t), file)

	waitOn(t, ownerAsks(t, rig.h))

	// At the grace question the review agent and the notice have run, and the take has not.
	assert.Len(t, rig.h.agents.seen(), 1, "the review of the pull request is the only agent step")
	assert.Equal(t, "opus", rig.h.agents.seen()[0].Model, "the project file names no steps: take_review runs on its default model")
	assert.Contains(t, rig.h.agents.seen()[0].Prompt, "belongs to somebody else")
	assert.Equal(t, []string{"--apply"}, flagsOf(rig.h.commands.requests), "only the notice has run")

	answerTheGrace(t, rig, "RELEASE")
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, []string{"--apply", "--take"}, flagsOf(rig.h.commands.requests))
}

func TestExampleTake_KeepFromTheAuthorStopsTheTakeAndNoClaimIsTried(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)
	done := runTake(rig.h.engine(t), file)

	answerTheGrace(t, rig, "KEEP")
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, []string{"--apply"}, flagsOf(rig.h.commands.requests), "the notice ran and the take did not")
}

func TestExampleTake_NoAnswerInTheGraceTimeTakesTheItem(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)

	outcome := receive(t, runTake(graceOverEngine(t, rig.h), file))

	require.NoError(t, outcome.err)
	assert.Equal(t, []string{"--apply", "--take"}, flagsOf(rig.h.commands.requests), "the clock ran out with no answer, so the take ran")
}

func TestExampleTake_ARefusalOfTheTakeForRepliesWithNoTokenHoldsTheItem(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if slices.Contains(request.Run, "--take") {
			return CommandOutput{RC: 2, Stdout: "REFUSE: 2 replies landed and none used KEEP or RELEASE. Read the intent and decide."}, nil
		}

		return CommandOutput{}, nil
	}

	outcome := receive(t, runTake(graceOverEngine(t, rig.h), file))

	require.NoError(t, outcome.err)
	require.NotNil(t, outcome.result.Stopped)
	assert.Equal(t, 2, outcome.result.Stopped.RC)
	assert.Contains(t, outcome.result.Stopped.Output, "none used KEEP or RELEASE")
	assert.NotContains(t, rig.h.commands.requests[1].Run, "--reply-read", "no step of the workflow says the replies were read")
}

func TestExampleTake_TheIdleAndGraceHoursAreOne(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)
	done := runTake(rig.h.engine(t), file)

	answerTheGrace(t, rig, "RELEASE")
	require.NoError(t, receive(t, done).err)

	first := strings.Join(rig.h.commands.requests[0].Run, " ")
	assert.Equal(t, "remedy take 7 --me owner --idle 1 --grace 1 --apply", first)
	assert.True(t, strings.HasSuffix(strings.Join(rig.h.commands.requests[1].Run, " "), "--take"), "the take is the last word of the call")
}

func TestExampleTake_ARefusedNoticeEndsTheItemBeforeTheGraceQuestion(t *testing.T) {
	t.Parallel()

	rig, file := takeRig(t)
	rig.h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{RC: 2, Stdout: "REFUSE: not stale"}, nil
	}

	outcome := receive(t, runTake(rig.h.engine(t), file))

	require.NoError(t, outcome.err)
	require.NotNil(t, outcome.result.Stopped)
	assert.Equal(t, 2, outcome.result.Stopped.RC)
	assert.Len(t, rig.h.commands.requests, 1, "a refused notice means no take is tried")
}
