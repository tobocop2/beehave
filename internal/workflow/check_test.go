package workflow_test

import (
	"strings"
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

	return workflow.Check(filesWith(text), "w.hcl", withForge)
}

// checkWith checks text for a project with the given capabilities.
func checkWith(t *testing.T, text string, caps workflow.Capabilities) []hclfile.Diagnostic {
	t.Helper()

	found, unparsed := workflow.Check(filesWith(text), "w.hcl", caps)
	require.False(t, unparsed)

	return found
}

// withForge is the project `shop`: the fields of a tracker and a forge together, and a forge.
var withForge = workflow.Capabilities{
	Project: "shop",
	Fields:  []string{"id", "title", "body", "branch", "head_sha", "pr", "author"},
	Forge:   true,
}

// noForge is the project `shop` with a tracker only.
var noForge = workflow.Capabilities{
	Project:   "shop",
	Fields:    []string{"id", "title", "body"},
	ForgeOnly: []string{"branch", "head_sha", "pr", "author"},
}

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
    inputs      = { head = gate.check.head, report = gate.check.report, session = agent.implement.session }
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
	assert.Contains(t, d.Text, "agent, command, env, gate, item, loop, merge, wait")
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
	assert.Contains(t, d.Text, "head_sha, session, text, transcript, violations")
}

func TestForbidCommandsWithAnUnknownKindIsRefusedAtItsPosition(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
agent "implement" {
  prompt          = "prompts/fix.md"
  forbid_commands = ["bulk-staging", "bulk-stagin"]
}
`)

	assert.Equal(t, 4, d.Line)
	assert.Equal(t, 38, d.Col)
	assert.Contains(t, d.Text, "unknown kind `bulk-stagin`")
	assert.Contains(t, d.Text, "Did you mean `bulk-staging`?")
	assert.Contains(t, d.Text, "bulk-staging, delete-text, local-delete, opaque-command, remote-delete, unparsed")
}

func TestForbidCommandsThatIsNotAListOfKindsIsRefused(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
agent "implement" {
  prompt          = "prompts/fix.md"
  forbid_commands = "bulk-staging"
}
`)

	assert.Equal(t, 4, d.Line)
	assert.Contains(t, d.Text, "forbid_commands must be a list of kinds")
}

func TestForbidCommandsWithKnownKindsAndAComputedListPass(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
agent "implement" {
  prompt          = "prompts/fix.md"
  forbid_commands = ["bulk-staging", "local-delete"]
}
agent "other" {
  prompt          = "prompts/fix.md"
  forbid_commands = [env.KIND]
}
agent "none" {
  prompt          = "prompts/fix.md"
  forbid_commands = []
}
`)

	assert.Empty(t, found)
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

	found, unparsed := workflow.Check(filesWith(text), "w.hcl", workflow.Capabilities{Fields: workflow.CommonItemFields()})

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

	found, _ := workflow.Check(fsys, "sub/w.hcl", withForge)
	assert.Empty(t, found)

	found, _ = workflow.Check(fsys, "other/w.hcl", withForge)
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

	found, unparsed := workflow.Check(fsys, "sub/w.hcl", withForge)

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
  run = gate.g.passed
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

	found, unparsed := workflow.Check(fsys, "w.hcl", withForge)

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

	found, unparsed := workflow.Check(fstest.MapFS{}, "none.hcl", withForge)

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

func TestCheckReadsAMergeStepAndItsOutputs(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
gate "check" {}
merge "ship" {
  head  = gate.check.head
  merge = false
  when  = gate.check.passed
}
command "after" {
  run = ["echo", merge.ship.state, merge.ship.reason]
}
`)

	assert.Empty(t, found)
}

// mergeSettings is a merge step with every setting that `merge = true` needs. A test replaces one line of it.
const mergeSettings = `workflow "w" {}
merge "ship" {
  head  = "abc"
  merge = true
  repo  = "owner/name"
  base  = "main"
  own   = { logins = [env.OWNER_LOGIN], emails = [env.OWNER_EMAIL] }
}
`

func TestCheckPassesAMergeStepWithEverySetting(t *testing.T) {
	t.Parallel()

	found, unparsed := check(t, `workflow "w" {}
agent "review" {
  prompt  = "prompts/fix.md"
  outputs = ["verdict"]
}
merge "ship" {
  head        = "abc"
  merge       = true
  repo        = env.REPO
  base        = "main"
  hold        = "hold"
  own         = { logins = [env.OWNER_LOGIN], emails = [env.OWNER_EMAIL] }
  forbid_text = ["noreply@anthropic", "Generated with \\[Claude"]
  approved    = agent.review.verdict == "approve"
  children    = "retarget"
}
`)

	assert.Empty(t, found)
	assert.False(t, unparsed)
}

func TestCheckReadsTheApprovedSettingOfAMergeStepFromAnotherStep(t *testing.T) {
	t.Parallel()

	found, unparsed := check(t, `workflow "w" {}
agent "review" {
  prompt  = "prompts/fix.md"
  outputs = ["verdict"]
}
merge "ship" {
  head     = "abc"
  approved = agent.review.verdict == "approve"
}
`)

	assert.Empty(t, found)
	assert.False(t, unparsed)

	d := oneProblem(t, `workflow "w" {}
merge "ship" {
  head     = "abc"
  approved = agent.nope.verdict == "approve"
}
`)

	assert.Contains(t, d.Text, "nope")
}

func TestCheckRefusesTheThirdPartySettingThatNoLongerExists(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, mergeSettings[:len(mergeSettings)-2]+"  third_party = \"refuse\"\n}\n")

	assert.Contains(t, d.Text, "third_party")
	assert.Equal(t, 8, d.Line)
}

// ownLine is the line of `own` in mergeSettings.
const (
	ownLine = "  own   = { logins = [env.OWNER_LOGIN], emails = [env.OWNER_EMAIL] }\n"
	ownAt   = 7
)

func TestCheckRefusesAnOwnThatIsNotAnObjectOfLoginsAndEmails(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ own, text string }{
		"a string":          {`"tobias"`, "object"},
		"a list":            {`["tobias"]`, "object"},
		"a typo key":        {`{ login = ["a"], emails = ["b"] }`, "`login`"},
		"an extra key":      {`{ logins = ["a"], emails = ["b"], names = [] }`, "`names`"},
		"no emails":         {`{ logins = ["a"] }`, "`emails`"},
		"no logins":         {`{ emails = ["a"] }`, "`logins`"},
		"a string list":     {`{ logins = "a", emails = ["b"] }`, "`logins`"},
		"a number item":     {`{ logins = [1], emails = ["b"] }`, "`logins`"},
		"a nested list":     {`{ logins = ["a"], emails = [["b"]] }`, "`emails`"},
		"two empty lists":   {`{ logins = [], emails = [] }`, "empty"},
		"a computed key":    {`{ (env.K) = ["a"], emails = ["b"] }`, "`logins`"},
		"a bool for emails": {`{ logins = ["a"], emails = true }`, "`emails`"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			text := strings.Replace(mergeSettings, ownLine, "  own   = "+tc.own+"\n", 1)
			d := oneProblem(t, text)

			assert.Contains(t, d.Text, tc.text)
			assert.Equal(t, ownAt, d.Line)
		})
	}
}

func TestCheckPointsAnOwnProblemAtTheValueThatIsWrong(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, strings.Replace(mergeSettings, ownLine, "  own   = { logins = [\"a\", 2], emails = [\"b\"] }\n", 1))

	assert.Equal(t, ownAt, d.Line)
	assert.Equal(t, 28, d.Col, "the problem sits on the item")
}

func TestCheckReadsAnOwnThatAnExpressionComputesAtRunTime(t *testing.T) {
	t.Parallel()

	for _, own := range []string{"env.OWN", `{ logins = env.L, emails = [env.E] }`, `{ logins = ["a"], emails = ["b"] }`} {
		found, _ := check(t, strings.Replace(mergeSettings, ownLine, "  own   = "+own+"\n", 1))

		assert.Empty(t, found, own)
	}
}

func TestCheckPassesAMergeTrueStepWithTheMinimumSettings(t *testing.T) {
	t.Parallel()

	found, unparsed := check(t, mergeSettings)

	assert.Empty(t, found)
	assert.False(t, unparsed)
}

func TestCheckRefusesALiteralMergeTrueWithASettingAbsent(t *testing.T) {
	t.Parallel()

	for _, setting := range []string{"repo", "base", "own"} {
		t.Run(setting, func(t *testing.T) {
			t.Parallel()

			text := strings.Replace(mergeSettings, "  "+setting+" ", "  # "+setting+" ", 1)
			d := oneProblem(t, text)

			assert.Contains(t, d.Text, "`"+setting+"`")
			assert.Equal(t, 2, d.Line, "the problem sits on the merge block header")
		})
	}
}

func TestCheckRefusesAMergeTrueStepWithNoSettingAndNamesEach(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
merge "ship" {
  head  = "abc"
  merge = true
}
`)

	require.Len(t, found, 3, "%v", found)
}

func TestCheckLeavesAMergeStepThatDoesNotMergeWithoutSettings(t *testing.T) {
	t.Parallel()

	for _, merge := range []string{"merge = false", "merge = env.GO", ""} {
		found, _ := check(t, "workflow \"w\" {}\nmerge \"ship\" {\n  head = \"abc\"\n  "+merge+"\n}\n")

		assert.Empty(t, found, merge)
	}
}

func TestCheckRefusesAnApprovedThatReadsNothing(t *testing.T) {
	t.Parallel()

	for _, literal := range []string{"true", "false", "1 == 1"} {
		d := oneProblem(t, mergeSettings[:len(mergeSettings)-2]+"  approved = "+literal+"\n}\n")

		assert.Contains(t, d.Text, "approved reads nothing", literal)
	}
}

func TestCheckRefusesAnApprovedThatReadsNoAgentOutput(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"env.OK", "env.OK == \"yes\"", "!env.HOLD"} {
		d := oneProblem(t, mergeSettings[:len(mergeSettings)-2]+"  approved = "+source+"\n}\n")

		assert.Contains(t, d.Text, "approved reads no agent output", source)
	}
}

func TestCheckRefusesAHoldLabelWithAComma(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, mergeSettings[:len(mergeSettings)-2]+"  hold = \"hold,wip\"\n}\n")

	assert.Contains(t, d.Text, "comma")
	assert.Equal(t, 8, d.Line)
	assert.Equal(t, 10, d.Col)
}

func TestCheckRefusesAForbidTextItemThatIsEmptyOrNotAPattern(t *testing.T) {
	t.Parallel()

	cases := map[string]string{"empty": `""`, "unclosed": `"a(b"`}
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := oneProblem(t, mergeSettings[:len(mergeSettings)-2]+"  forbid_text = [\"ok\", "+item+"]\n}\n")

			assert.Contains(t, d.Text, "forbid_text")
			assert.Equal(t, 8, d.Line)
			assert.Equal(t, 24, d.Col, "the problem sits on the item")
		})
	}
}

func TestCheckRefusesAForbidTextItemThatIsNotAString(t *testing.T) {
	t.Parallel()

	for name, item := range map[string]string{"a number": "7", "a list": `["a"]`, "a bool": "true"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := oneProblem(t, mergeSettings[:len(mergeSettings)-2]+"  forbid_text = [\"ok\", "+item+"]\n}\n")

			assert.Contains(t, d.Text, "forbid_text")
			assert.Contains(t, d.Text, "string")
			assert.Equal(t, 8, d.Line)
			assert.Equal(t, 24, d.Col, "the problem sits on the item")
		})
	}
}

func TestCheckRefusesAForbidTextThatIsNotAList(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, mergeSettings[:len(mergeSettings)-2]+"  forbid_text = \"abc\"\n}\n")

	assert.Contains(t, d.Text, "list")
	assert.Equal(t, 8, d.Line)
}

func TestCheckRefusesAnUnknownChildrenValue(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, mergeSettings[:len(mergeSettings)-2]+"  children = \"always\"\n}\n")

	assert.Contains(t, d.Text, "`always`")
	assert.Contains(t, d.Text, "Use one of")
	assert.Equal(t, 8, d.Line)
}

func TestCheckReadsAMergeSettingThatAnExpressionComputesAtRunTime(t *testing.T) {
	t.Parallel()

	found, _ := check(t, mergeSettings[:len(mergeSettings)-2]+"  hold = env.HOLD\n  forbid_text = [env.PATTERN]\n  children = env.KIND\n}\n")

	assert.Empty(t, found)
}

func TestCheckRefusesAnUnknownEnvNameInAMergeSetting(t *testing.T) {
	t.Parallel()

	d := oneProblem(t, `workflow "w" {}
merge "ship" {
  head = "abc"
  repo = nope.REPO
}
`)

	assert.Contains(t, d.Text, "nope")
}

func TestCheckRejectsAnUnknownMergeOutputAndAnUnknownMergeArgument(t *testing.T) {
	t.Parallel()

	found, _ := check(t, `workflow "w" {}
merge "ship" {
  head = "abc"
}
command "after" {
  run = [merge.ship.nope]
}
`)

	require.Len(t, found, 1)
	assert.Contains(t, found[0].Text, "nope")

	missing, _ := check(t, "workflow \"w\" {}\nmerge \"ship\" {}\n")

	require.Len(t, missing, 1)
	assert.Contains(t, missing[0].Text, "`head` is required")
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

const mergeStep = `workflow "w" {}
gate "check" {}
merge "ship" {
  head  = gate.check.head
  merge = false
}
`

func TestAMergeStepInAProjectWithNoForgeIsRefusedAtTheStepLabel(t *testing.T) {
	t.Parallel()

	found := checkWith(t, mergeStep, noForge)

	require.Len(t, found, 1, "%v", found)
	assert.Equal(t, 3, found[0].Line)
	assert.Equal(t, "step `merge.ship` needs a forge, and project `shop` has none. Set `forge = \"github\"` in the project file, or remove the step.", found[0].Text)
}

func TestAMergeStepInAProjectWithAForgeIsNotRefused(t *testing.T) {
	t.Parallel()

	assert.Empty(t, checkWith(t, mergeStep, withForge))
}

func TestAWorkflowWithNoMergeStepNeedsNoForge(t *testing.T) {
	t.Parallel()

	assert.Empty(t, checkWith(t, lintAndFix, noForge))
}

func TestTheForgeRefusalComesBeforeTheMergeSettingsOfTheSameStep(t *testing.T) {
	t.Parallel()

	found := checkWith(t, strings.Replace(mergeSettings, `  repo  = "owner/name"
`, "", 1), noForge)

	require.Len(t, found, 2, "%v", found)
	assert.Contains(t, found[0].Text, "needs a forge")
	assert.Contains(t, found[1].Text, "no `repo`")
}

func TestAForgeFieldInAProjectWithNoForgeSaysThatTheProjectHasNoForge(t *testing.T) {
	t.Parallel()

	found := checkWith(t, twoSteps(`
command "use" {
  run = [item.pr]
}
`), noForge)

	require.Len(t, found, 1, "%v", found)
	assert.Equal(t, "w.hcl:7:10: `item` has no field `pr`. The project has no forge, and only a forge declares `pr`. Use one of: body, id, kind, title.", found[0].Error())
}

func TestAnUnknownFieldInAProjectWithNoForgeDoesNotBlameTheForge(t *testing.T) {
	t.Parallel()

	found := checkWith(t, twoSteps(`
command "use" {
  run = [item.sha]
}
`), noForge)

	require.Len(t, found, 1, "%v", found)
	assert.NotContains(t, found[0].Text, "no forge")
}

func TestEveryStepKindAnswersWhetherItNeedsAForge(t *testing.T) {
	t.Parallel()

	steps := map[workflow.Kind]workflow.Step{
		workflow.KindAgent:   &workflow.Agent{},
		workflow.KindCommand: &workflow.Command{},
		workflow.KindGate:    &workflow.Gate{},
		workflow.KindLoop:    &workflow.Loop{},
		workflow.KindMerge:   &workflow.Merge{},
		workflow.KindWait:    &workflow.Wait{},
	}

	for kind, step := range steps {
		assert.Equal(t, kind == workflow.KindMerge, step.NeedsForge(), kind)
	}
}
