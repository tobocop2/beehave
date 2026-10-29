package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localsFlow reads functions and locals in a `when`, an `until` and the `inputs` of an agent.
// The item title is "a title" and its id is "12".
const localsFlow = `workflow "w" {}

locals {
  label  = format("%s/%s", lower(item.title), item.id)
  titled = contains(split(" ", item.title), "title")
}

command "prepare" {
  run  = ["prepare", local.label]
  when = local.titled && regex_match("^[0-9]+$", item.id)
}

loop "tidy" {
  max   = 3
  until = local.clean

  locals {
    clean = command.lint.rc == 0 && length(["a"]) == 1
    note  = join("-", ["round", format("%d", loop.round)])
  }

  command "lint" {
    run = ["lint", local.note]
  }

  command "fix" {
    run  = ["fix", local.note]
    when = !local.clean
  }
}

agent "report" {
  prompt = "prompts/a.md"
  when   = coalesce(null, true)
  inputs = { what = local.label }
}
`

func TestWalk_FunctionsAndLocalsWorkInWhenUntilAndInputs(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.commands.reply = lintRCs(1, 1, 0)

	result, err := rig.run(t, localsFlow, writeTo)

	require.NoError(t, err)
	assert.Equal(t, ItemResult{Item: testItem}, result)
	assert.Equal(t, []string{"prepare a title/12", "lint round-1", "fix round-1", "lint round-2", "fix round-2", "lint round-3"}, rig.h.commands.ran(),
		"the loop stops right after the lint that its local names, so the third fix does not run")

	seen := rig.h.agents.seen()
	require.Len(t, seen, 1)
	assert.Contains(t, seen[0].Prompt, "Fix a title/12.")
}

func TestWalk_ALocalThatTheConditionOfALoopReadsDecidesWhereTheCheckRuns(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = lintRCs(0)

	_, err := walkText(t, h, `workflow "w" {}
loop "tidy" {
  max   = 3
  until = local.clean

  locals {
    clean = command.lint.rc == 0
  }

  command "lint" {
    run = ["lint"]
  }

  command "fix" {
    run = ["fix"]
  }
}
`)

	require.NoError(t, err)
	assert.Equal(t, []string{"lint"}, h.commands.ran(), "until follows the lint step that the local reads, so the fix is not run")
}

func TestWalk_AFunctionThatFailsFailsTheStepWithThePositionOfTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := walkText(t, h, "workflow \"w\" {}\ncommand \"a\" {\n  run  = [\"a\"]\n  when = regex_match(\"(\", item.id)\n}\n")

	require.ErrorContains(t, err, "step a#1")
	require.ErrorContains(t, err, "cannot be evaluated")
}

// nestedLocalsFlow has a local of the outer loop that an inner loop reads. The wait holds the run in round 2 of the outer loop.
const nestedLocalsFlow = `workflow "w" {}

loop "outer" {
  max   = 2
  until = command.mark.rc == 99

  locals {
    r = loop.round
    p = coalesce(loop.previous.command.mark.rc, 0)
  }

  command "mark" {
    run = ["mark"]
  }

  wait "ask" {
    question = "go?"
    when     = loop.round == 2
  }

  loop "inner" {
    max   = 3
    until = command.in.rc == 99

    command "in" {
      run = ["in", format("outer=%d previous=%d inner=%d", local.r, local.p, loop.round)]
    }
  }
}
`

// nestedLocalsRan is what the commands of nestedLocalsFlow run. The inner loop reads the round and the previous round of the outer loop.
var nestedLocalsRan = []string{
	"mark",
	"in outer=1 previous=0 inner=1", "in outer=1 previous=0 inner=2", "in outer=1 previous=0 inner=3",
	"mark",
	"in outer=2 previous=7 inner=1", "in outer=2 previous=7 inner=2", "in outer=2 previous=7 inner=3",
}

func markRC7(request CommandRequest) (CommandOutput, error) {
	if request.Run[0] == "mark" {
		return CommandOutput{RC: 7}, nil
	}

	return CommandOutput{}, nil
}

func TestWalk_ALocalOfAnOuterLoopKeepsTheRoundAndThePreviousRoundOfThatLoopWhenAnInnerLoopReadsIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = markRC7

	file := writeFlow(t, nestedLocalsFlow)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "go") == nil })

	require.NoError(t, (<-done).err)
	assert.Equal(t, nestedLocalsRan, h.commands.ran(), "inner round 3 under outer round 2 reads outer round 2, and the previous round of the outer loop")
}

func TestWalk_AReplayGivesTheSameValuesForALocalOfAnOuterLoopThatAnInnerLoopReads(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = markRC7

	file := writeFlow(t, nestedLocalsFlow)
	interrupted(t, h, file)

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "go") == nil })

	require.NoError(t, (<-done).err)
	assert.Equal(t, nestedLocalsRan, h.commands.ran(), "the commands of round 1 ran once and the replay runs round 2 with the same values")
}

const localsReplayFlow = `workflow "w" {}

locals {
  shout = format("%s-%s", lower(item.title), command.before.stdout)
}

command "before" {
  run = ["before"]
}

wait "ask" {
  question = "go?"
}

command "after" {
  run = ["after", local.shout, lower(wait.ask.answer)]
}
`

func TestWalk_AReplayAfterACrashGivesTheSameValueForAFunctionCallAndForALocal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		return CommandOutput{Stdout: "OUT-" + request.Run[0]}, nil
	}

	file := writeFlow(t, localsReplayFlow)
	interrupted(t, h, file)

	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{Stdout: "CHANGED"}, nil
	}

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "YES") == nil })

	outcome := <-done

	require.NoError(t, outcome.err)
	assert.Equal(t, []string{"before", "after a title-OUT-before yes"}, h.commands.ran(),
		"the local is rebuilt from the output that the first run recorded, and lower() gives the same text")
}
