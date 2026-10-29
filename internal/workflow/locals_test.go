package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"

	"github.com/tobocop2/obee/internal/workflow"
)

const localsPrefix = `workflow "w" {}
`

// scopeUnderTest is a scope with the locals of a workflow file.
type scopeUnderTest struct {
	scope *workflow.Scope
	flow  *workflow.Workflow
}

// loop returns the first loop of the file.
func (s *scopeUnderTest) loop(t *testing.T) *workflow.Loop {
	t.Helper()

	for _, step := range s.flow.Steps {
		if loop, ok := step.(*workflow.Loop); ok {
			return loop
		}
	}

	require.FailNow(t, "the file has no loop")

	return nil
}

func TestLoadReadsTheLocalsOfTheFileAndOfALoopInFileOrder(t *testing.T) {
	t.Parallel()

	flow, err := load(t, localsPrefix+`
locals {
  b = 1
  a = 2
}
locals {
  c = 3
}
loop "l" {
  max   = 1
  until = true
  locals {
    d = local.a
  }
  command "x" { run = ["true"] }
}
`)

	require.NoError(t, err)
	require.Len(t, flow.Locals, 3)
	assert.Equal(t, "b", flow.Locals[0].Name)
	assert.Equal(t, "a", flow.Locals[1].Name)
	assert.Equal(t, "c", flow.Locals[2].Name)
	assert.Equal(t, 8, flow.Locals[2].Range.Start.Line)

	loop, ok := flow.Steps[0].(*workflow.Loop)
	require.True(t, ok)
	require.Len(t, loop.Locals, 1)
	assert.Equal(t, "d", loop.Locals[0].Name)
}

func TestLoadRefusesALocalDeclaredTwiceInTheFile(t *testing.T) {
	t.Parallel()

	d := only(t, localsPrefix+"locals {\n  a = 1\n}\nlocals {\n  a = 2\n}\n")

	assert.Equal(t, 6, d.Line)
	assert.Equal(t, "local `a` is declared twice; the first is on line 3. Rename one.", d.Text)
}

func TestLoadRefusesALoopLocalThatHidesALocalOfTheFile(t *testing.T) {
	t.Parallel()

	d := only(t, localsPrefix+"locals {\n  a = 1\n}\nloop \"l\" {\n  max = 1\n  until = true\n  locals {\n    a = 2\n  }\n}\n")

	assert.Equal(t, 9, d.Line)
	assert.Contains(t, d.Text, "local `a` is declared twice; the first is on line 3.")
}

func TestLoadAllowsTheSameLocalNameInTwoLoops(t *testing.T) {
	t.Parallel()

	_, err := load(t, localsPrefix+"loop \"p\" {\n  max = 1\n  until = true\n  locals {\n    a = 1\n  }\n}\nloop \"q\" {\n  max = 1\n  until = true\n  locals {\n    a = 2\n  }\n}\n")

	require.NoError(t, err)
}

func TestLoadRefusesALocalsBlockWithANameOrABlock(t *testing.T) {
	t.Parallel()

	_, err := load(t, localsPrefix+"locals \"x\" {\n  inner {\n  }\n}\n")
	found := problems(t, err)

	require.Len(t, found, 2)
	assert.Equal(t, "`locals` takes no name. Write `locals { }`.", found[0].Text)
	assert.Equal(t, "`locals` holds names and no blocks. Move the block out of `locals`.", found[1].Text)
}

func TestEnvNamesIncludesTheNamesThatLocalsRead(t *testing.T) {
	t.Parallel()

	flow, err := load(t, localsPrefix+`
locals {
  a = env.TOP
}
loop "l" {
  max   = 1
  until = true
  locals {
    b = env.INNER
  }
  command "x" { run = ["true"] }
}
`)

	require.NoError(t, err)
	assert.Equal(t, []string{"INNER", "TOP"}, flow.EnvNames())
}

func TestCheckRefusesAnUnknownFunctionWithItsPositionAndTheKnownOnes(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps("command \"c\" {\n  run  = [\"true\"]\n  when = upper(item.id) == \"X\"\n}\n"))

	assert.Equal(t, 7, d.Line)
	assert.Equal(t, 10, d.Col)
	assert.Contains(t, d.Text, "unknown function `upper`.")
	assert.Contains(t, d.Text, "Use one of: coalesce, contains, format, join, length, lower, regex_match, split.")
}

func TestCheckRefusesAnUnknownFunctionInALocal(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps("locals {\n  a = nope(1)\n}\n"))

	assert.Equal(t, 6, d.Line)
	assert.Contains(t, d.Text, "unknown function `nope`.")
}

func TestCheckPassesEveryFunctionAndEveryKindOfLocal(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
locals {
  title = lower(item.title)
  both  = "${local.title}-${item.id}"
}
agent "implement" {
  prompt  = "prompts/fix.md"
  outputs = ["verdict"]
  when    = contains(split(",", env.LABELS), "go") && regex_match("^p-", item.id)
  inputs  = { name = format("%s/%d", local.both, length(["a"])), joined = join(",", ["a", "b"]), first = coalesce("", local.title) }
}
loop "rounds" {
  max   = 3
  until = local.latest == ""

  locals {
    latest = loop.round == 1 ? agent.implement.verdict : loop.previous.agent.fix.verdict
  }

  agent "fix" {
    prompt  = "prompts/fix.md"
    outputs = ["verdict"]
    when    = local.latest != ""
  }
}
`)

	assert.Empty(t, found)
}

func TestCheckRefusesALocalThatNamesNoLocalAndOffersTheNearest(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps("locals {\n  latest = 1\n}\ncommand \"c\" {\n  run = [\"true\"]\n  when = local.latst == 1\n}\n"))

	assert.Equal(t, 10, d.Line)
	assert.Equal(t, 10, d.Col)
	assert.Contains(t, d.Text, "unknown local `latst`. Did you mean `latest`? Use one of: latest.")
}

func TestCheckReportsAMissingLocalOnceWhereALocalReadsItAndNotAtEachReader(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps("locals {\n  a = local.ghost\n}\ncommand \"c\" {\n  run = [\"true\"]\n  when = local.a == 1\n}\n"))

	assert.Equal(t, 6, d.Line)
	assert.Contains(t, d.Text, "unknown local `ghost`.")
}

func TestCheckSaysThatNoLocalIsVisible(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps("command \"c\" {\n  run  = [\"true\"]\n  when = local.x == 1\n}\n"))

	assert.Equal(t, "unknown local `x`. No local is visible here. Declare it in a `locals` block.", d.Text)
}

func TestCheckRefusesALocalWithNoName(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps("locals {\n  a = 1\n}\ncommand \"c\" {\n  run  = [\"true\"]\n  stdin = local\n}\n"))

	assert.Equal(t, "`local` needs a name. Write `local.<name>`. Use one of: a.", d.Text)
}

func TestCheckShowsACycleOfLocalsAsAChain(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps("locals {\n  a = local.b\n  b = local.a\n}\n"))

	assert.Equal(t, 6, d.Line)
	assert.Equal(t, "cycle `a -> b -> a`. Remove one of the references.", d.Text)
}

func TestCheckShowsALocalThatReadsItselfAsAChain(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps("locals {\n  a = local.a\n}\n"))

	assert.Equal(t, "cycle `a -> a`. Remove one of the references.", d.Text)
}

func TestCheckRefusesALoopLocalReadOutsideItsLoop(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`loop "l" {
  max   = 1
  until = true
  locals {
    inner = 1
  }
  command "x" { run = ["true"] }
}
command "after" {
  run  = ["true"]
  when = local.inner == 1
}
`))

	assert.Equal(t, 15, d.Line)
	assert.Equal(t, "local `inner` belongs to loop `l` and is read outside it. Read it inside that loop, or declare it at the top of the file.", d.Text)
}

func TestCheckRefusesALocalThatReadsAStepThatRunsLaterThanTheReader(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
locals {
  late = command.after.rc
}
command "first" {
  run  = ["true"]
  when = local.late == 0
}
command "after" {
  run = ["true"]
}
`)

	assert.Equal(t, 7, d.Line)
	assert.Equal(t, 10, d.Col)
	assert.Equal(t, "`local.late` reads `command.after`, which runs later than this reference. Move the step up or remove the reference.", d.Text)
}

func TestCheckFollowsALocalThatReadsALaterStepThroughAnotherLocal(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
locals {
  deep  = command.after.rc
  outer = local.deep
}
command "first" {
  run  = ["true"]
  when = local.outer == 0
}
command "after" {
  run = ["true"]
}
`)

	assert.Contains(t, d.Text, "`local.outer` reads `command.after`, which runs later")
}

func TestCheckAllowsTheConditionOfALoopToReadTheBodyThroughALocal(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
locals {
  ok = gate.check.passed
}
loop "l" {
  max   = 2
  until = local.ok
  gate "check" {}
}
`)

	assert.Empty(t, found)
}

func TestCheckRefusesALoopLocalConditionThatReadsALaterStepOutsideTheLoop(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
loop "l" {
  max   = 1
  until = local.v == 0
  locals {
    v = command.tail.rc
  }
  gate "check" {}
}
command "tail" {
  run = ["true"]
}
`)

	assert.Equal(t, 4, d.Line)
	assert.Contains(t, d.Text, "`local.v` reads `command.tail`, which runs later than this reference.")
}

func TestCheckRefusesLoopRoundInALocalOfTheFile(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, "workflow \"w\" {}\nlocals {\n  r = loop.round\n}\n")

	assert.Equal(t, 3, d.Line)
	assert.Contains(t, d.Text, "`loop.round` is used outside a loop.")
}

func TestCheckReportsAnUnknownStepInALocalOnceAtTheLocal(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps("locals {\n  a = agent.ghost.text\n}\ncommand \"c\" {\n  run = [\"true\"]\n  when = local.a == \"\"\n}\n"))

	assert.Equal(t, 6, d.Line)
	assert.Contains(t, d.Text, "unknown agent `ghost`.")
}

func TestCheckShowsACycleBetweenAStepAndALocalItReads(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
locals {
  v = command.b.rc
}
command "a" {
  run  = ["true"]
  when = local.v == 0
}
command "b" {
  run  = ["true"]
  when = command.a.rc == 0
}
`)

	var texts []string
	for _, d := range found {
		texts = append(texts, d.Text)
	}

	assert.Contains(t, texts, "cycle `a -> b -> a`. Remove one of the references.")
}

func newLocalScope(t *testing.T, file string) *scopeUnderTest {
	t.Helper()

	flow, err := load(t, file)
	require.NoError(t, err)

	scope, _ := newScope(t)
	scope.Define(flow.Locals)

	return &scopeUnderTest{scope: scope, flow: flow}
}

func TestScopeEvaluatesALocalFromTheOutputsAtEachRead(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, localsPrefix+"locals {\n  base = item.id\n  tag  = \"${local.base}-x\"\n}\ncommand \"lint\" { run = [\"true\"] }\n")

	got, err := s.scope.Value(expression(t, `local.tag`), 0)

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("p-1-x"), got)
}

func TestScopeDoesNotEvaluateALocalThatNothingReads(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, localsPrefix+"locals {\n  broken = item.id + 1\n  fine   = item.id\n}\n")

	got, err := s.scope.Value(expression(t, `local.fine`), 0)

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("p-1"), got)
}

func TestScopeReadsTheOutputsThatAreRecordedWhenALocalIsRead(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, localsPrefix+"locals {\n  rc = command.lint.rc\n}\n")
	steps := scopeSteps(t)

	_, err := s.scope.Value(expression(t, `local.rc`), 0)
	require.ErrorContains(t, err, "`command.lint` has not run yet.")

	s.scope.Record(steps["lint"], map[string]cty.Value{"rc": cty.NumberIntVal(3)})

	got, err := s.scope.Value(expression(t, `local.rc`), 0)
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(3), got)

	s.scope.Record(steps["lint"], map[string]cty.Value{"rc": cty.NumberIntVal(4)})

	got, err = s.scope.Value(expression(t, `local.rc`), 0)
	require.NoError(t, err)
	assert.Equal(t, cty.NumberIntVal(4), got, "a local holds no value: the next read sees the new output")
}

func TestScopeEvaluatesALocalThatTwoReadersShareOnceInTheRightOrder(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, localsPrefix+"locals {\n  base = 2\n  left = local.base + 1\n  right = local.base + 2\n}\n")

	got, err := s.scope.Value(expression(t, `local.left + local.right + local.base`), 0)

	require.NoError(t, err)
	assert.True(t, cty.NumberIntVal(9).RawEquals(got), "%#v", got)
}

func TestScopeNamesTheLocalThatCannotBeEvaluated(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, localsPrefix+"locals {\n  broken = item.id + 1\n}\n")

	_, err := s.scope.Value(expression(t, `local.broken`), 0)

	require.ErrorContains(t, err, "local `broken` cannot be evaluated: ")
}

func TestScopeRefusesALocalThatDoesNotExist(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, localsPrefix)

	_, err := s.scope.Value(expression(t, `local.nope`), 0)
	require.ErrorContains(t, err, "`local.nope` names no local that this expression can read.")

	_, err = s.scope.Value(expression(t, `local`), 0)
	require.ErrorContains(t, err, "`local.` names no local")
}

func TestScopeRefusesALoopOfLocals(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, localsPrefix+"locals {\n  a = local.b\n  b = local.a\n}\n")

	_, err := s.scope.Value(expression(t, `local.a`), 0)

	require.ErrorContains(t, err, "cycle `a -> b -> a`")
}

func TestScopeReadsTheLocalsOfALoopOnlyInsideIt(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, localsPrefix+"loop \"rounds\" {\n  max = 2\n  until = true\n  locals {\n    n = loop.round * 10\n  }\n  command \"inner\" { run = [\"true\"] }\n}\n")
	loop := s.loop(t)

	leave := s.scope.EnterLoop(loop)
	s.scope.EndRound(loop)

	got, err := s.scope.Value(expression(t, `local.n`), 2)
	require.NoError(t, err)
	assert.True(t, cty.NumberIntVal(20).RawEquals(got), "%#v", got)

	leave()

	_, err = s.scope.Value(expression(t, `local.n`), 0)
	require.ErrorContains(t, err, "`local.n` names no local")
}

func TestScopeReadsAnOuterLocalFromAnInnerLoop(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, localsPrefix+"locals {\n  top = 1\n}\nloop \"rounds\" {\n  max = 2\n  until = true\n  locals {\n    inner = local.top + 1\n  }\n  command \"inner_step\" { run = [\"true\"] }\n}\n")

	s.scope.EnterLoop(s.loop(t))

	got, err := s.scope.Value(expression(t, `local.inner`), 1)

	require.NoError(t, err)
	assert.True(t, cty.NumberIntVal(2).RawEquals(got), "%#v", got)
}

const nestedScopeFile = localsPrefix + `loop "outer" {
  max   = 3
  until = true

  locals {
    r = loop.round
    p = loop.previous.command.mark.rc
  }

  command "mark" { run = ["true"] }

  loop "inner" {
    max   = 4
    until = true

    locals {
      own = loop.round
    }

    command "step" { run = ["true"] }
  }
}
`

func TestScopeEvaluatesALocalInTheLoopThatDeclaresItWhenAnInnerLoopReadsIt(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, nestedScopeFile)
	outer := s.loop(t)

	inner, ok := outer.Steps[1].(*workflow.Loop)
	require.True(t, ok)

	s.scope.EnterLoop(outer)
	s.scope.Record(outer.Steps[0], map[string]cty.Value{"rc": cty.NumberIntVal(5)})
	s.scope.EndRound(outer)

	s.scope.EnterLoop(inner)
	s.scope.EndRound(inner)
	s.scope.EndRound(inner)

	got, err := s.scope.Value(expression(t, `[local.r, local.p, local.own, loop.round]`), 3)

	require.NoError(t, err)
	assert.True(t, cty.TupleVal([]cty.Value{cty.NumberIntVal(2), cty.NumberIntVal(5), cty.NumberIntVal(3), cty.NumberIntVal(3)}).RawEquals(got),
		"the outer local sees outer round 2 and the outer previous round, the inner local sees inner round 3: %#v", got)
}

func TestScopeGivesALocalOfTheFileNoLoopFrame(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, localsPrefix+"locals {\n  top = loop.round\n}\n"+"loop \"rounds\" {\n  max = 2\n  until = true\n  command \"inner_step\" { run = [\"true\"] }\n}\n")
	s.scope.EnterLoop(s.loop(t))
	s.scope.EndRound(s.loop(t))

	_, err := s.scope.Value(expression(t, `local.top`), 2)

	require.ErrorContains(t, err, "local `top` cannot be evaluated", "a local of the file is not evaluated in the frame of the loop that reads it")
}

func TestScopeReadsListsTheNamesThroughLocals(t *testing.T) {
	t.Parallel()

	s := newLocalScope(t, localsPrefix+"locals {\n  a = command.lint.rc\n  b = \"${local.a}${agent.fix.text}\"\n}\n")

	var roots []string
	for _, tr := range s.scope.Reads(expression(t, `local.b == item.id`)) {
		roots = append(roots, tr.RootName())
	}

	assert.ElementsMatch(t, []string{"item", "agent", "command"}, roots)
	assert.Empty(t, s.scope.Reads(expression(t, `local.missing`)), "a local that does not exist adds nothing")
}

func TestScopeCallsEachFunction(t *testing.T) {
	t.Parallel()

	scope, _ := newScope(t)

	cases := map[string]cty.Value{
		`contains(["a", "hold"], "hold")`:        cty.True,
		`contains(["a"], "hold")`:                cty.False,
		`length(["a", "b"])`:                     cty.NumberIntVal(2),
		`lower("AbC")`:                           cty.StringVal("abc"),
		`join("-", ["a", "b"])`:                  cty.StringVal("a-b"),
		`split(",", "a,b")`:                      cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}),
		`regex_match("^p-[0-9]+$", item.id)`:     cty.True,
		`regex_match("^q-", item.id)`:            cty.False,
		`coalesce(null, "x", "y")`:               cty.StringVal("x"),
		`coalesce("", "x")`:                      cty.StringVal(""),
		`format("%s:%03d", item.id, 7)`:          cty.StringVal("p-1:007"),
		`contains(split(",", "a,hold"), "hold")`: cty.True,
	}

	for text, want := range cases {
		got, err := scope.Value(expression(t, text), 0)

		require.NoError(t, err, text)
		assert.True(t, want.RawEquals(got), "%s gave %#v", text, got)
	}
}

func TestScopeRefusesARegexMatchWithAPatternThatIsNotRE2(t *testing.T) {
	t.Parallel()

	scope, _ := newScope(t)

	_, err := scope.Value(expression(t, `regex_match("(", "x")`), 0)

	require.ErrorContains(t, err, "cannot be evaluated")
}
