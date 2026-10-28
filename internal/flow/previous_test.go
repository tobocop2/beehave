package flow

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// previousFlow has a step that reads the step before it in the round before, and a step that reads itself.
// A step answers a value that names its call: m1, m2 and so on for `mark`.
const previousFlow = `workflow "w" {}
loop "l" {
  max   = 3
  until = loop.round == 3

  command "read" {
    run = ["read", loop.previous.command.mark.stdout == null ? "none" : loop.previous.command.mark.stdout]
  }

  command "mark" {
    run  = ["mark", loop.previous.command.mark.stdout == null ? "none" : loop.previous.command.mark.stdout]
    when = %s
  }
}
`

// markReplies answers each `mark` call with the next value m1, m2 and so on. Any other command answers nothing.
func markReplies() func(CommandRequest) (CommandOutput, error) {
	calls := 0

	return func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] != "mark" {
			return CommandOutput{}, nil
		}

		calls++

		return CommandOutput{Stdout: fmt.Sprintf("m%d", calls)}, nil
	}
}

func TestWalk_APreviousReadIsNullInRoundOneAndReadsTheRecordedRoundAfter(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = markReplies()

	_, err := walkText(t, h, fmt.Sprintf(previousFlow, "true"))

	require.NoError(t, err)
	assert.Equal(t, []string{
		"read none", "mark none",
		"read m1", "mark m1",
		"read m2", "mark m2",
	}, h.commands.ran(), "round 1 reads null; a later step and the step itself read the round before")
}

func TestWalk_APreviousReadOfAStepThatAWhenSkippedIsNull(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = markReplies()

	_, err := walkText(t, h, fmt.Sprintf(previousFlow, "loop.round != 2"))

	require.NoError(t, err)
	assert.Equal(t, []string{
		"read none", "mark none",
		"read m1",
		"read none", "mark none",
	}, h.commands.ran(), "round 3 reads the skipped mark of round 2 as null")
}

const previousCrashFlow = `workflow "w" {}
loop "l" {
  max   = 3
  until = loop.round == 3

  command "mark" {
    run = ["mark", loop.previous.command.mark.stdout == null ? "none" : loop.previous.command.mark.stdout]
  }

  wait "ask" {
    question = "go?"
    when     = loop.round == 2
  }
}
`

func TestWalk_AReplayAfterACrashInRoundTwoReadsTheSameValuesOfTheRoundBefore(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = markReplies()
	file := writeFlow(t, previousCrashFlow)
	interrupted(t, h, file)

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "yes") == nil })

	outcome := <-done

	require.NoError(t, outcome.err)
	assert.Equal(t, []string{"mark none", "mark m1", "mark m2"}, h.commands.ran(),
		"rounds 1 and 2 ran once, before the crash; round 3 reads the recorded value of round 2")
}
