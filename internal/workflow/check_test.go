package workflow_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/hclfile"
	"github.com/tobocop2/obee/internal/workflow"
)

const fixPrompt = "Fix the lint output in {{ .lint }}.\n"

func filesWith(text string) fstest.MapFS {
	return fstest.MapFS{
		"w.hcl":          {Data: []byte(text)},
		"prompts/fix.md": {Data: []byte(fixPrompt)},
	}
}

func check(t *testing.T, text string) ([]hclfile.Diagnostic, bool) {
	t.Helper()

	return workflow.Check(filesWith(text), "w.hcl", forgeFields)
}

// forgeFields is the field list of a tracker and a forge together.
var forgeFields = []string{"id", "title", "body", "branch", "head_sha", "pr", "author"}

// oneProblem checks text and wants exactly one problem.
func oneProblem(t *testing.T, text string) hclfile.Diagnostic {
	t.Helper()

	found, unparsed := check(t, text)
	require.False(t, unparsed)
	require.Len(t, found, 1, "%v", found)

	return found[0]
}

func twoSteps(second string) string {
	return `workflow "w" {}
agent "implement" {
  prompt = "prompts/fix.md"
}
` + second
}

func TestCheckPassesTheLintAndFixFixture(t *testing.T) {
	t.Parallel()

	found, unparsed := check(t, lintAndFix)

	assert.Empty(t, found)
	assert.False(t, unparsed)
}

func TestCheckPassesEveryKindOfReference(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
agent "implement" {
  prompt  = "prompts/fix.md"
  outputs = ["verdict"]
  when    = item.id != env.MODE
}
gate "check" {}
wait "ask" {
  question = agent.implement.text
}
loop "rounds" {
  max   = 3
  until = gate.check.passed && agent.implement.verdict == "ok" && wait.ask.answer == "go" && loop.round > 1

  agent "review" {
    prompt      = "prompts/fix.md"
    resume_from = loop.round <= 3 ? agent.implement : null
    when        = loop.rounds.exhausted
    inputs      = { head = gate.check.head, rc = gate.check.rc, session = agent.implement.session }
  }
}
command "after" {
  run  = ["echo", agent.review.text, loop.rounds.exhausted]
  when = true ? true : false
}
`)

	assert.Empty(t, found)
}

func TestCheckAcceptsAnyOutputWhenOutputsAreComputed(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
agent "implement" {
  prompt  = "prompts/fix.md"
  outputs = split(",", "a,b")
}
command "use" {
  run = [agent.implement.anything]
}
`)
	assert.Empty(t, found)
}

func TestUnknownRootNamesTheNearestAndTheValidRoots(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [itme.id]
}
`))

	assert.Equal(t, 7, d.Line)
	assert.Equal(t, 10, d.Col)
	assert.Equal(t, "w.hcl", d.File)
	assert.Contains(t, d.Text, "unknown name `itme`")
	assert.Contains(t, d.Text, "Did you mean `item`?")
	assert.Contains(t, d.Text, "agent, command, env, gate, item, loop, wait")
}

func TestUnknownRootWithNothingCloseOffersNoGuess(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [zzzzzzzzzz.id]
}
`))

	assert.NotContains(t, d.Text, "Did you mean")
}

func TestUnknownStepNamesTheNearestStep(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [agent.implment.text]
}
`))

	assert.Equal(t, 7, d.Line)
	assert.Contains(t, d.Text, "unknown agent `implment`")
	assert.Contains(t, d.Text, "Did you mean `implement`?")
	assert.Contains(t, d.Text, "Use one of: implement.")
}

func TestUnknownStepWhenTheFileHasNoStepOfThatKind(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [gate.check.passed]
}
`))

	assert.Contains(t, d.Text, "unknown gate `check`")
	assert.Contains(t, d.Text, "The file has no gate")
}

func TestAStepNameOfAnotherKindIsUnknown(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [command.implement.rc]
}
`))

	assert.Contains(t, d.Text, "unknown command `implement`")
}

func TestUnknownOutputListsTheValidOutputs(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [agent.implement.txt]
}
`))

	assert.Contains(t, d.Text, "agent `implement` has no output `txt`")
	assert.Contains(t, d.Text, "Did you mean `text`?")
	assert.Contains(t, d.Text, "head_sha, session, text, transcript")
}

func TestDeclaredOutputsAreValid(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
agent "implement" {
  prompt  = "prompts/fix.md"
  outputs = ["verdict", "score"]
}
command "use" {
  run = [agent.implement.verdict, agent.implement.score]
}
`)

	assert.Empty(t, found)
}

func TestAReferenceWithoutAnOutputIsReported(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [agent.implement]
}
`))

	assert.Contains(t, d.Text, "name an output")
	assert.Contains(t, d.Text, "agent.implement")
}

func TestResumeFromMustNameAnAgent(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
command "c" {
  run = ["true"]
}
agent "a" {
  prompt      = "prompts/fix.md"
  resume_from = command.c
}
`)

	assert.Equal(t, 7, d.Line)
	assert.Contains(t, d.Text, "names an agent")
}

func TestResumeFromWithAnOutputIsStillChecked(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
agent "b" {
  prompt      = "prompts/fix.md"
  resume_from = agent.implement.nope
}
`))

	assert.Contains(t, d.Text, "has no output `nope`")
}

func TestAnIndexedReferenceIsReported(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [agent["implement"].text]
}
`))

	assert.Contains(t, d.Text, "agent.<name>.<output>")
}

func TestAStepThatRunsLaterIsReported(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
command "first" {
  run = [command.second.stdout]
}
command "second" {
  run = ["true"]
}
`)

	assert.Equal(t, 3, d.Line)
	assert.Contains(t, d.Text, "`command.second` runs later")
}

func TestAReferenceFromInsideALoopToAStepAfterTheLoopRunsLater(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
loop "l" {
  max   = 2
  until = command.in.rc == 0

  command "in" {
    run = [command.out.stdout]
  }
}
command "out" {
  run = ["true"]
}
`)

	assert.Equal(t, 7, d.Line)
	assert.Contains(t, d.Text, "runs later")
}

func TestAReferenceFromALoopStepToALaterSiblingRunsLater(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
loop "l" {
  max   = 2
  until = command.b.rc == 0

  command "a" {
    run = [command.b.stdout]
  }
  command "b" {
    run = ["true"]
  }
}
`)

	assert.Equal(t, 7, d.Line)
}

func TestAReferenceToTheEnclosingLoopIsAllowed(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
loop "l" {
  max   = 2
  until = command.a.rc == 0

  command "a" {
    run  = ["true"]
    when = loop.l.exhausted
  }
}
`)

	assert.Empty(t, found)
}

func TestLoopRoundOutsideALoopIsReported(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [loop.round]
}
`))

	assert.Equal(t, 7, d.Line)
	assert.Contains(t, d.Text, "`loop.round` is used outside a loop")
}

func TestLoopRoundInsideALoopIsAllowed(t *testing.T) {
	t.Parallel()

	found, _ := check(t, lintAndFix)

	assert.Empty(t, found)
}

func TestABareLoopReferenceIsReported(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [loop]
}
`))

	assert.Contains(t, d.Text, "loop.round")
}

func TestLoopExhaustedNeedsALoopName(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [loop.implement.exhausted]
}
`))

	assert.Contains(t, d.Text, "unknown loop `implement`")
}

func TestALoopHasOnlyTheExhaustedOutput(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
loop "l" {
  max   = 2
  until = true

  command "a" {
    run = ["true"]
  }
}
command "use" {
  run = [loop.l.done]
}
`)

	assert.Contains(t, d.Text, "loop `l` has no output `done`")
	assert.Contains(t, d.Text, "Use one of: exhausted.")
}

func TestUnknownItemFieldListsTheFields(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, twoSteps(`
command "use" {
  run = [item.titel]
}
`))

	assert.Contains(t, d.Text, "`item` has no field `titel`")
	assert.Contains(t, d.Text, "Did you mean `title`?")
	assert.Contains(t, d.Text, "author, body, branch, head_sha, id, kind, pr, title")
}

func TestAnItemFieldThatTheAdaptersDoNotDeclareIsRefusedWithThePositionAndTheDeclaredNames(t *testing.T) {
	t.Parallel()

	text := twoSteps(`
command "use" {
  run = [item.branch]
}
`)

	found, unparsed := workflow.Check(filesWith(text), "w.hcl", workflow.CommonItemFields())

	require.False(t, unparsed)
	require.Len(t, found, 1)
	assert.Equal(t, "w.hcl:7:10: `item` has no field `branch`. Use one of: body, id, kind, title.", found[0].Error())
}

func TestCommonItemFieldsAreTheIdTheTitleAndTheBody(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"id", "title", "body"}, workflow.CommonItemFields())
}

func TestABareItemOrEnvReferenceIsReported(t *testing.T) {
	t.Parallel()

	found, _ := check(t, twoSteps(`
command "use" {
  run = [item, env]
}
`))

	require.Len(t, found, 2)
	assert.Contains(t, found[0].Text, "item.<field>")
	assert.Contains(t, found[1].Text, "env.<NAME>")
}

func TestEveryReferenceProblemIsReportedInPositionOrder(t *testing.T) {
	t.Parallel()

	found, _ := check(t, twoSteps(`
command "use" {
  run  = [itme.id, agent.nope.text]
  when = agent.implement.nope
}
`))

	require.Len(t, found, 3)
	assert.Equal(t, []int{7, 7, 8}, []int{found[0].Line, found[1].Line, found[2].Line})
	assert.Less(t, found[0].Col, found[1].Col)
}

func TestAStepThatReferencesItselfIsACycle(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
command "a" {
  run = [command.a.stdout]
}
`)

	assert.Equal(t, 2, d.Line)
	assert.Contains(t, d.Text, "cycle `a -> a`")
}

func TestTwoStepsThatNeedEachOtherAreACycleAndAForwardReference(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
command "a" {
  run = [command.b.stdout]
}
command "b" {
  run = [command.a.stdout]
}
`)

	require.Len(t, found, 2)
	assert.Contains(t, found[0].Text, "cycle `a -> b -> a`")
	assert.Equal(t, 2, found[0].Line)
	assert.Contains(t, found[1].Text, "`command.b` runs later")
}

func TestACycleThroughThreeStepsIsNamedInOrder(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
command "a" {
  run = [command.b.stdout]
}
command "b" {
  run = [command.c.stdout]
}
command "c" {
  run = [command.a.stdout]
}
`)

	var cycles []string

	for _, d := range found {
		if len(d.Text) > 5 && d.Text[:5] == "cycle" {
			cycles = append(cycles, d.Text)
		}
	}

	require.Len(t, cycles, 1)
	assert.Contains(t, cycles[0], "`a -> b -> c -> a`")
}

func TestALoopThatNamesItsBodyIsNotACycle(t *testing.T) {
	t.Parallel()

	found, _ := check(t, lintAndFix)

	assert.Empty(t, found)
}

func TestTwoReferencesToTheSameStepGiveOneEdge(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
command "a" {
  run = ["true"]
}
command "b" {
  run  = [command.a.stdout]
  when = command.a.rc == 0
}
`)

	assert.Empty(t, found)
}

func TestMissingPromptFileIsReportedAtTheValue(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
agent "a" {
  prompt = "prompts/none.md"
}
`)

	assert.Equal(t, 3, d.Line)
	assert.Equal(t, 12, d.Col)
	assert.Contains(t, d.Text, "prompt file `prompts/none.md` is missing")
}

func TestPromptPathIsRelativeToTheWorkflowFile(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"sub/w.hcl":      {Data: []byte("workflow \"w\" {}\nagent \"a\" {\n  prompt = \"p.md\"\n}\n")},
		"sub/p.md":       {Data: []byte("hello")},
		"other/w.hcl":    {Data: []byte("workflow \"w\" {}\nagent \"a\" {\n  prompt = \"p.md\"\n}\n")},
		"p.md":           {Data: []byte("not this one")},
		"other/note.txt": {Data: []byte("x")},
	}

	found, _ := workflow.Check(fsys, "sub/w.hcl", forgeFields)
	assert.Empty(t, found)

	found, _ = workflow.Check(fsys, "other/w.hcl", forgeFields)
	require.Len(t, found, 1)
	assert.Equal(t, "other/w.hcl", found[0].File)
}

func TestAPromptThatIsNotAFileIsReported(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
agent "a" {
  prompt = "prompts"
}
`)

	assert.Contains(t, d.Text, "cannot read prompt file `prompts`")
}

func TestAPromptThatEscapesTheFolderIsReported(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
agent "a" {
  prompt = "../p.md"
}
`)

	assert.Contains(t, d.Text, "prompt file `../p.md` must be a relative path inside the workflow folder")
}

func TestAPromptThatClimbsOutOfASubfolderIsReported(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"sub/w.hcl": {Data: []byte("workflow \"w\" {}\nagent \"a\" {\n  prompt = \"../p.md\"\n}\n")},
		"p.md":      {Data: []byte("outside")},
	}

	found, unparsed := workflow.Check(fsys, "sub/w.hcl", forgeFields)

	require.False(t, unparsed)
	require.Len(t, found, 1, "%v", found)
	assert.Equal(t, 3, found[0].Line)
	assert.Contains(t, found[0].Text, "prompt file `../p.md` must be a relative path inside the workflow folder")
}

func TestAnAbsolutePromptPathIsReportedAsOutsideTheFolder(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
agent "a" {
  prompt = "/prompts/fix.md"
}
`)

	assert.Contains(t, d.Text, "prompt file `/prompts/fix.md` must be a relative path inside the workflow folder")
}

func TestAReferenceErrorAndAnUnknownKeyAreBothReported(t *testing.T) {
	t.Parallel()

	found, unparsed := check(t, `workflow "w" {}
agent "a" {
  prompt  = "prompts/fix.md"
  bogus   = 1
}
command "c" {
  run = agent.missing.text
}
`)

	require.False(t, unparsed)
	require.Len(t, found, 2, "%v", found)
	assert.Contains(t, found[0].Text, "bogus")
	assert.Contains(t, found[1].Text, "unknown agent `missing`")
}

func TestAReferenceToAStepThatFailedToLoadReportsOnlyThatStep(t *testing.T) {
	t.Parallel()

	found, unparsed := check(t, `workflow "w" {}
agent "a" {
  prompt = "prompts/fix.md"
  bogus  = 1
}
command "c" {
  run = agent.a.text
}
loop "l" {
  max   = 0
  until = gate.g.passed
  gate "g" {
    bogus = 1
  }
}
command "d" {
  run = gate.g.rc
}
`)

	require.False(t, unparsed)
	require.Len(t, found, 3, "%v", found)

	for _, d := range found {
		assert.NotRegexp(t, "unknown (agent|gate|command|loop|wait)", d.Text)
	}
}

func TestATemplateThatDoesNotParseIsReported(t *testing.T) {
	t.Parallel()

	fsys := filesWith("workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/fix.md\"\n}\n")
	fsys["prompts/fix.md"] = &fstest.MapFile{Data: []byte("{{ .open ")}

	found, unparsed := workflow.Check(fsys, "w.hcl", forgeFields)

	require.Len(t, found, 1)
	assert.False(t, unparsed)
	assert.Equal(t, 3, found[0].Line)
	assert.Contains(t, found[0].Text, "prompt file `prompts/fix.md` is not a valid template")
}

func TestAPromptThatIsComputedIsNotChecked(t *testing.T) {
	t.Parallel()

	found, _ := check(t, twoSteps(`
agent "b" {
  prompt = item.kind == "fix" ? "prompts/none.md" : "prompts/none2.md"
}
agent "c" {
  prompt = 42
}
`))

	assert.Empty(t, found)
}

func TestCheckMarksAFileThatDoesNotParse(t *testing.T) {
	t.Parallel()

	found, unparsed := workflow.Check(fstest.MapFS{}, "none.hcl", forgeFields)

	require.Len(t, found, 1)
	assert.True(t, unparsed)
	assert.Contains(t, found[0].Text, "cannot read the file")

	found, unparsed = check(t, "workflow \"a\" {\n  x = \n}\n")

	assert.True(t, unparsed)
	assert.NotEmpty(t, found)
}

func TestCheckReportsStructuralProblemsAndStopsThere(t *testing.T) {
	t.Parallel()

	found, unparsed := check(t, `workflow "w" {}
agent "a" {
  prompt = "prompts/none.md"
  bogus  = 1
}
`)

	assert.False(t, unparsed)
	require.Len(t, found, 1)
	assert.Contains(t, found[0].Text, "unknown key `bogus`")
}

func TestAnOutputsListWithAReferenceAcceptsAnyOutput(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
agent "implement" {
  prompt  = "prompts/fix.md"
  outputs = [item.id]
}
command "use" {
  run = [agent.implement.anything]
}
`)

	assert.Empty(t, found)
}
