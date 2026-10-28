package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"

	"github.com/tobocop2/obee/internal/workflow"
)

func TestPreviousReadsInTheSameLoopAreAllowedForAnyStepOfIt(t *testing.T) {
	t.Parallel()

	found, unparsed := check(t, `workflow "w" {}
loop "l" {
  max   = 3
  until = loop.previous.command.b.rc == 0 && command.b.rc == 0

  command "a" {
    run = ["a", loop.previous.command.a.stdout, loop.previous.command.b.stdout]
  }

  command "b" {
    run  = ["b", loop.previous.command.a.stdout, loop.previous.command.b.stdout]
    when = loop.previous.command.b.rc != 1
  }
}
`)

	require.False(t, unparsed)
	assert.Empty(t, found, "the step itself, an earlier step, a later step and the until of the loop")
}

func TestPreviousOutsideALoopIsRefusedAtItsPosition(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
command "a" {
  run = ["a"]
}
command "b" {
  run = [loop.previous.command.a.stdout]
}
`)

	assert.Equal(t, 6, d.Line)
	assert.Equal(t, "`loop.previous` is used outside a loop. Put the step in a `loop` block.", d.Text)
}

func TestPreviousOfAStepOfAnotherLoopIsRefused(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
loop "one" {
  max   = 2
  until = true

  command "a" {
    run = ["a"]
  }
}
loop "two" {
  max   = 2
  until = true

  command "b" {
    run = [loop.previous.command.a.stdout]
  }
}
`)

	assert.Equal(t, 15, d.Line)
	assert.Equal(t, "`loop.previous` reads a step of the same loop, and `command.a` is not in this loop. Name a step of this loop.", d.Text)
}

func TestPreviousOfAStepOutsideEveryLoopOrOfTheLoopItselfIsRefused(t *testing.T) {
	t.Parallel()

	for _, reference := range []string{"loop.previous.command.top.stdout", "loop.previous.loop.l.exhausted"} {
		d := oneProblem(t, `workflow "w" {}
command "top" {
  run = ["top"]
}
loop "l" {
  max   = 2
  until = true

  command "in" {
    run = [`+reference+`]
  }
}
`)

		assert.Equal(t, 10, d.Line, reference)
		assert.Contains(t, d.Text, "is not in this loop", reference)
	}
}

func TestPreviousAcrossNestedLoopsReadsOnlyTheClosestLoop(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
loop "outer" {
  max   = 2
  until = true

  command "o" {
    run = ["o", loop.previous.loop.inner.exhausted]
  }

  loop "inner" {
    max   = 2
    until = true

    command "i" {
      run = ["i", loop.previous.command.i.stdout, loop.previous.command.o.stdout]
    }
  }
}
`)

	require.Len(t, found, 1, "%v", found)
	assert.Equal(t, 15, found[0].Line, "the inner step may not read the outer step")
	assert.Contains(t, found[0].Text, "`command.o` is not in this loop")
}

func TestPreviousNeedsAStepAnOutputAndAKnownName(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"loop.previous":                          "`loop.previous` needs a step. Write `loop.previous.<kind>.<name>.<output>`.",
		"loop.previous.command":                  "`loop.previous` needs a step. Write `loop.previous.<kind>.<name>.<output>`.",
		"loop.previous.item.id":                  "`loop.previous` needs a step. Write `loop.previous.<kind>.<name>.<output>`.",
		"loop.previous.command.a":                "`loop.previous.command.a` must name an output. Write `loop.previous.command.a.<output>`.",
		"loop.previous.command.a.nope":           "command `a` has no output `nope`",
		"loop.previous.command.zzz.stdout":       "unknown command `zzz`",
		"loop.previous.agent.a.stdout":           "unknown agent `a`",
		"loop.previous.command.a.stdout == null": "",
	}

	for reference, want := range cases {
		found, _ := check(t, `workflow "w" {}
loop "l" {
  max   = 2
  until = true

  command "a" {
    run = [`+reference+`]
  }
}
`)

		if want == "" {
			assert.Empty(t, found, reference)

			continue
		}

		require.Len(t, found, 1, "%s: %v", reference, found)
		assert.Equal(t, 7, found[0].Line, reference)
		assert.Contains(t, found[0].Text, want, reference)
	}
}

func TestPreviousOfAnAgentOutputNamesTheDeclaredOutputs(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
loop "l" {
  max   = 2
  until = true

  agent "b" {
    prompt  = "prompts/fix.md"
    outputs = ["verdict"]
    inputs = { v = loop.previous.agent.b.verdict, w = loop.previous.agent.b.violations, x = loop.previous.agent.b.nope }
  }
}
`)

	require.Len(t, found, 1, "%v", found)
	assert.Contains(t, found[0].Text, "agent `b` has no output `nope`")
}

func TestPreviousIsNoNameForResumeFrom(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
agent "implement" {
  prompt = "prompts/fix.md"
}
loop "l" {
  max   = 2
  until = true

  agent "b" {
    prompt      = "prompts/fix.md"
    resume_from = loop.previous.agent.b.session
  }
}
`)

	assert.Equal(t, 11, d.Line)
	assert.Equal(t, "`resume_from` names an agent of this round. Write `agent.<name>`; `loop.previous` is no agent.", d.Text)
}

func TestPreviousAddsNoOrderAndNoCycle(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
loop "l" {
  max   = 2
  until = true

  command "a" {
    run = ["a", loop.previous.command.b.stdout]
  }

  command "b" {
    run = ["b", loop.previous.command.a.stdout]
  }
}
`)

	assert.Empty(t, found, "a read of the round before is no cycle and does not run later")
}

func TestScopeGivesLoopPreviousTypedNullsInRoundOneAndTheEndOfTheRoundAfter(t *testing.T) {
	t.Parallel()

	scope, steps := newScope(t)
	loop, ok := steps["rounds"].(*workflow.Loop)
	require.True(t, ok)

	leave := scope.EnterLoop(loop)

	got, err := scope.Value(expression(t, `loop.previous.command.inner.rc`), 1)
	require.NoError(t, err)
	assert.True(t, got.IsNull())
	assert.Equal(t, cty.Number, got.Type())

	scope.Record(steps["inner"], map[string]cty.Value{"rc": cty.NumberIntVal(3)})

	got, err = scope.Value(expression(t, `loop.previous.command.inner.rc`), 1)
	require.NoError(t, err)
	assert.True(t, got.IsNull(), "a record of this round does not change the round before")

	scope.EndRound(loop)

	got, err = scope.Value(expression(t, `loop.previous.command.inner.rc`), 2)
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.NumberIntVal(3)))

	leave()

	_, err = scope.Value(expression(t, `loop.previous.command.inner.rc`), 0)
	require.Error(t, err, "outside the loop there is no loop.previous")
}

func TestScopeLeavingAnInnerLoopGivesBackThePreviousRoundOfTheOuterLoop(t *testing.T) {
	t.Parallel()

	scope, steps := newScope(t)
	loop, ok := steps["rounds"].(*workflow.Loop)
	require.True(t, ok)

	leaveOuter := scope.EnterLoop(loop)
	scope.Record(steps["inner"], map[string]cty.Value{"rc": cty.NumberIntVal(7)})
	scope.EndRound(loop)

	leaveInner := scope.EnterLoop(loop)

	got, err := scope.Value(expression(t, `loop.previous.command.inner.rc`), 1)
	require.NoError(t, err)
	assert.True(t, got.IsNull(), "a loop that starts has no round before")

	leaveInner()

	got, err = scope.Value(expression(t, `loop.previous.command.inner.rc`), 1)
	require.NoError(t, err)
	assert.True(t, got.RawEquals(cty.NumberIntVal(7)))

	leaveOuter()
}
