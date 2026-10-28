package flow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

// gitTree makes a folder with one commit and returns the folder and the commit.
func gitTree(t *testing.T) (dir, head string) {
	t.Helper()

	dir = t.TempDir()

	initRepo := exec.CommandContext(t.Context(), "git", "init", "-q")
	initRepo.Dir = dir
	out, err := initRepo.CombinedOutput()
	require.NoError(t, err, string(out))

	commit := exec.CommandContext(t.Context(), "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "one")
	commit.Dir = dir
	out, err = commit.CombinedOutput()
	require.NoError(t, err, string(out))

	rev := exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD")
	rev.Dir = dir
	out, err = rev.Output()
	require.NoError(t, err)

	return dir, strings.TrimSpace(string(out))
}

// loginsUntil picks the test login for the first ok calls, which the engine makes before the first step, and fails every call after them.
type loginsUntil struct {
	mu    sync.Mutex
	calls int
	ok    int
	later error
}

func (l *loginsUntil) Pick(context.Context, string, string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.calls++

	if l.calls <= l.ok {
		return testLogin, nil
	}

	return "", l.later
}

func (l *loginsUntil) PickFresh(context.Context, string, string) (string, error) { return "", nil }

// outputPath returns the path that a rendered prompt names after the words "Write to ".
func outputPath(prompt string) string {
	_, rest, _ := strings.Cut(prompt, "Write to ")
	path, _, _ := strings.Cut(rest, "\n")

	return path
}

// agentRig is one run of a workflow file with an agent step over a git folder.
type agentRig struct {
	h    *harness
	dir  string
	head string

	mu     sync.Mutex
	failed []error
}

// put writes the output file of a fake agent. A failed write is kept, and run reports it.
func (r *agentRig) put(path, body string) {
	err := errors.Join(os.MkdirAll(filepath.Dir(path), 0o750), os.WriteFile(path, []byte(body), 0o600))

	r.mu.Lock()
	defer r.mu.Unlock()

	r.failed = append(r.failed, err)
}

func newAgentRig(t *testing.T) *agentRig {
	t.Helper()

	dir, head := gitTree(t)
	h := newHarness(t)
	h.trees = &fakeTrees{dir: dir}

	return &agentRig{h: h, dir: dir, head: head}
}

// reply makes the fake agent write body at the output path of its prompt and end with result.
func (r *agentRig) reply(body string, result agent.Result) {
	r.h.agents.result = func(request AgentRequest) agent.Result {
		if path := outputPath(request.Prompt); body != "" && path != "" {
			r.put(path, body)
		}

		return result
	}
}

// run writes the flow and the prompt file beside it and walks the flow.
func (r *agentRig) run(t *testing.T, flow, prompt string) (ItemResult, error) {
	t.Helper()

	file := writeFlow(t, flow)
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte(prompt), 0o600))

	result, err := r.h.engine(t).RunFile(testItem, testProject, file)

	r.mu.Lock()
	defer r.mu.Unlock()

	require.NoError(t, errors.Join(r.failed...), "the fake agent writes its output file")

	return result, err
}

const writeTo = "Write to {{ .output_file }}\nFix {{ .what }}.\n"

var exited = agent.Result{Outcome: agent.OutcomeExited, Session: "sess-1", Transcript: "/t.jsonl", Text: "all done"}

func TestAgentStep_RendersThePromptWithTheInputsAndTheOutputFile(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  inputs = { what = \"the lint\" }\n}\n", writeTo)

	require.NoError(t, err)

	seen := rig.h.agents.seen()
	require.Len(t, seen, 1)
	assert.Contains(t, seen[0].Prompt, "Fix the lint.")
	assert.Contains(t, outputPath(seen[0].Prompt), ".json")
	assert.Equal(t, testLogin, seen[0].Login)
}

func TestAgentStep_AnInputTheTemplateNeedsAndTheFlowLacksFailsTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n}\n", writeTo)

	require.ErrorContains(t, err, "step a#1")
	require.ErrorContains(t, err, "what")
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgentStep_AMissingPromptFileAndABadPromptArgumentAreRefused(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/none.md\"\n}\n", writeTo)
	require.ErrorContains(t, err, "none.md")

	rig = newAgentRig(t)
	_, err = rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = 3\n}\n", writeTo)
	require.ErrorContains(t, err, "must be a string")

	rig = newAgentRig(t)
	_, err = rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  inputs = 3\n}\n", "Write to {{ .output_file }}\n")
	require.ErrorContains(t, err, "inputs")

	rig = newAgentRig(t)
	_, err = rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n}\n", "{{ .output_file \n")
	require.ErrorContains(t, err, "a.md")
}

func TestAgentStep_TheModelAndTheTimeoutsGoToTheAgent(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  model = \"opus\"\n  runner = \"second\"\n  timeout = \"30m\"\n  idle_timeout = \"5m\"\n}\n", "Write to {{ .output_file }}\n")

	require.NoError(t, err)

	seen := rig.h.agents.seen()
	require.Len(t, seen, 1)
	assert.Equal(t, "opus", seen[0].Model)
	assert.Equal(t, 30*time.Minute, seen[0].Timeout)
	assert.Equal(t, 5*time.Minute, seen[0].IdleTimeout)
	assert.Equal(t, "second", seen[0].Login)
}

func TestAgentStep_ABadTimeoutAndALoginThatCannotBePickedFailTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  timeout = \"soon\"\n}\n", "x")
	require.ErrorContains(t, err, "not a duration")

	rig = newAgentRig(t)
	_, err = rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  idle_timeout = \"soon\"\n}\n", "x")
	require.ErrorContains(t, err, "not a duration")

	rig = newAgentRig(t)
	_, err = rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  model = 4\n}\n", "x")
	require.ErrorContains(t, err, "must be a string")

	rig = newAgentRig(t)
	rig.h.picker = &loginsUntil{ok: 2, later: errors.New("login gone")}
	_, err = rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n}\n", "Write to {{ .output_file }}\n")
	require.ErrorContains(t, err, "pick the agent login for item")
}

func TestAgentStep_AnArgumentThatCannotBeEvaluatedFailsTheStep(t *testing.T) {
	t.Parallel()

	for _, arg := range []string{"inputs = nope.x", "inputs = \"text\"", "resume_from = nope.x", "output_tries = nope.x"} {
		rig := newAgentRig(t)
		_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  "+arg+"\n}\n", "x")

		require.Error(t, err, arg)
		assert.Empty(t, rig.h.agents.seen(), arg)
	}
}

func TestAgentStep_ResumeFromAStepThatIsNotAnAgentRunOrWasSkippedStartsFresh(t *testing.T) {
	t.Parallel()

	for _, from := range []string{`"text"`, "agent.first"} {
		rig := newAgentRig(t)
		rig.reply("", exited)

		flow := "workflow \"w\" {}\nagent \"first\" {\n  prompt = \"prompts/a.md\"\n  when = false\n  inputs = { what = \"x\" }\n}\n" +
			"agent \"a\" {\n  prompt = \"prompts/a.md\"\n  inputs = { what = \"y\" }\n  resume_from = " + from + "\n}\n"

		_, err := rig.run(t, flow, writeTo)

		require.NoError(t, err)
		assert.Equal(t, []string{""}, []string{rig.h.agents.seen()[0].Resume}, from)
	}
}

func TestAgentStep_AnOldOutputFileThatCannotBeRemovedFailsTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	require.NoError(t, os.MkdirAll(filepath.Join(outputFile(rig.h.opts.StateDir, testItem, "a#1"), "inside"), 0o700))

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n}\n", "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "remove the old output file")
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgentStep_AFolderForTheOutputFileThatCannotBeMadeFailsTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	require.NoError(t, os.MkdirAll(rig.h.opts.StateDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(rig.h.opts.StateDir, outputsFolder), nil, 0o600))

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n}\n", "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "make the folder of the output file")
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgentStep_TheOutputFileIsInTheStateFolderAndTheWorktreeStaysClean(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"ok"}`, exited)

	_, err := rig.run(t, strings.Replace(outputsFlow, "%s", "1", 1), "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	status := exec.CommandContext(t.Context(), "git", "status", "--porcelain", "--untracked-files=all")
	status.Dir = rig.dir
	out, err := status.Output()
	require.NoError(t, err)
	assert.Empty(t, string(out), "the worktree has no new file")

	written, err := os.ReadFile(outputFile(rig.h.opts.StateDir, testItem, "a#1"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"verdict":"ok"}`, string(written))
}

func TestOutputFile_ASeparatorOrADotInAnItemOrAStepNeverMakesTwoPairsShareAFile(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	seen := map[string]bool{}

	for _, pair := range [][2]string{{"a/b", "c"}, {"a", "b/c"}, {"a.b", "c"}, {"a", "b.c"}, {"..", "a#1"}, {"a", "a#1"}} {
		path := outputFile(state, pair[0], pair[1])

		assert.Equal(t, filepath.Join(state, outputsFolder), filepath.Dir(path), "one folder holds every output file")
		assert.False(t, seen[path], "%v shares a file", pair)

		seen[path] = true
	}
}

func TestAgentStep_APinOnAFolderThatIsNotARepositoryFailsTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.trees = &fakeTrees{dir: t.TempDir()}

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  pin = \"abc\"\n}\n", "x")

	require.ErrorContains(t, err, "read HEAD of the item folder")
}

const resumeFlow = `workflow "w" {}
agent "first" {
  prompt = "prompts/a.md"
  inputs = { what = "x" }
}
agent "a" {
  prompt      = "prompts/a.md"
  inputs      = { what = "y" }
  resume_from = %s
}
`

func TestAgentStep_ResumeFromContinuesTheSessionOfTheNamedAgentAndNullStartsFresh(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, strings.Replace(resumeFlow, "%s", "agent.first", 1), writeTo)
	require.NoError(t, err)

	seen := rig.h.agents.seen()
	require.Len(t, seen, 2)
	assert.Empty(t, seen[0].Resume)
	assert.Equal(t, "sess-1", seen[1].Resume)

	rig = newAgentRig(t)
	rig.reply("", exited)

	_, err = rig.run(t, strings.Replace(resumeFlow, "%s", "null", 1), writeTo)
	require.NoError(t, err)
	assert.Empty(t, rig.h.agents.seen()[1].Resume)
}

func TestAgentStep_APinThatIsNotTheFolderHeadIsRefusedWithBothCommits(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  pin = \"abc123\"\n}\n", "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "abc123")
	require.ErrorContains(t, err, rig.head)
	assert.Empty(t, rig.h.agents.seen())

	rig = newAgentRig(t)
	rig.reply("", exited)
	_, err = rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  pin = \""+rig.head+"\"\n}\n", "Write to {{ .output_file }}\n")
	require.NoError(t, err)
}

const outputsFlow = `workflow "w" {}
agent "a" {
  prompt       = "prompts/a.md"
  outputs      = ["verdict"]
  output_tries = %s
}
command "show" {
  run = ["show", agent.a.verdict, agent.a.text, agent.a.session, agent.a.head_sha, agent.a.transcript]
}
`

func TestAgentStep_DeclaredOutputsComeFromTheFileAndTheKindOutputsFromTheRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"approve"}`, exited)

	_, err := rig.run(t, strings.Replace(outputsFlow, "%s", "1", 1), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"show approve all done sess-1 " + rig.head + " /t.jsonl"}, rig.h.commands.ran())
}

func TestAgentStep_AMissingOrIllTypedOutputNamesTheStepAndTheOutput(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]string{
		`{}`:               `step a#1: the agent did not write the output "verdict"`,
		`{"verdict":3}`:    `step a#1: the output "verdict" must be a string`,
		`not json`:         `step a#1: the output file`,
		`{"verdict":null}`: `step a#1: the agent did not write the output "verdict"`,
	} {
		rig := newAgentRig(t)
		rig.reply(body, exited)

		_, err := rig.run(t, strings.Replace(outputsFlow, "%s", "1", 1), "Write to {{ .output_file }}\n")

		require.ErrorContains(t, err, want)
		assert.Len(t, rig.h.agents.seen(), 1, "one try in all by default")
	}

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, strings.Replace(outputsFlow, "%s", "1", 1), "Write to {{ .output_file }}\n")
	require.ErrorContains(t, err, "the agent wrote no output file")
}

func TestAgentStep_OutputTriesRunTheAgentThatManyTimesInAllBeforeFailing(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{}`, exited)

	_, err := rig.run(t, strings.Replace(outputsFlow, "%s", "3", 1), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, `the agent did not write the output "verdict"`)
	assert.Len(t, rig.h.agents.seen(), 3)

	rig = newAgentRig(t)
	calls := 0
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		calls++

		if calls == 2 {
			rig.put(outputPath(request.Prompt), `{"verdict":"ok"}`)
		}

		return exited
	}

	_, err = rig.run(t, strings.Replace(outputsFlow, "%s", "3", 1), "Write to {{ .output_file }}\n")
	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 2, "the second try passes and ends the tries")
}

func TestAgentStep_AStepWithoutOutputTriesRunsTheAgentOnceInAll(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{}`, exited)

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt  = \"prompts/a.md\"\n  outputs = [\"verdict\"]\n}\n", "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, `the agent did not write the output "verdict"`)
	assert.Len(t, rig.h.agents.seen(), 1)
}

func TestAgentStep_ABadOutputTriesIsRefused(t *testing.T) {
	t.Parallel()

	for _, tries := range []string{`"two"`, `0`, `1.5`} {
		rig := newAgentRig(t)
		_, err := rig.run(t, strings.Replace(outputsFlow, "%s", tries, 1), "x")

		require.ErrorContains(t, err, "output_tries")
	}
}

func TestAgentStep_ARequiredSkillThatTheRunDidNotCallFailsTheStepAndNamesIt(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", agent.Result{Outcome: agent.OutcomeExited, Skills: []string{"tdd"}})

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  require_skills = [\"tdd\", \"review\"]\n}\n", "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, `the agent did not call the skill "review"`)
	assert.NotContains(t, err.Error(), `"tdd"`)

	rig = newAgentRig(t)
	rig.reply("", agent.Result{Outcome: agent.OutcomeExited, Skills: []string{"tdd", "review"}})

	_, err = rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  require_skills = [\"tdd\", \"review\"]\n}\n", "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	rig = newAgentRig(t)
	_, err = rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  require_skills = 3\n}\n", "x")
	require.ErrorContains(t, err, "list of strings")
}

func TestAgentStep_AnAgentThatDoesNotExitCleanlyFailsTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", agent.Result{Outcome: agent.OutcomeTimedOut, Note: "too slow"})

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n}\n", "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "too slow")
}

func TestAgentStep_AFolderThatCannotBeFoundFailsTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.trees = &fakeTrees{err: assert.AnError}

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n}\n", "x")
	require.ErrorIs(t, err, assert.AnError)

	rig = newAgentRig(t)
	rig.h.trees = &fakeTrees{dir: t.TempDir()}

	_, err = rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n}\n", "Write to {{ .output_file }}\n")
	require.ErrorContains(t, err, "HEAD")
}

func TestAgentStep_AnItemWithNoWorktreeGetsOneBeforeThePinAndTheHeadAreRead(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.trees = &fakeTrees{late: &lateFolder{dir: rig.dir}}
	rig.reply("", exited)

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  pin = \""+rig.head+"\"\n}\ncommand \"show\" {\n  run = [\"show\", agent.a.head_sha]\n}\n", "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show " + rig.head}, rig.h.commands.ran())
}

func TestAgentStep_AnEmptyFolderFromEnsureFailsTheStepBeforeTheAgentRuns(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.trees = &fakeTrees{}

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n}\n", "x")

	require.ErrorContains(t, err, "has no folder")
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgentStep_AReplayKeepsTheOutputFileOfAnAgentThatAlreadyRan(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{}`, exited)

	file := writeFlow(t, replayFlow)
	prompt := filepath.Join(filepath.Dir(file), "prompts", "a.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(prompt), 0o750))
	require.NoError(t, os.WriteFile(prompt, []byte("Write to {{ .output_file }}\n"), 0o600))

	interrupted(t, rig.h, file)
	require.FileExists(t, outputFile(rig.h.opts.StateDir, testItem, "a#1"))

	next := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "yes") == nil })
	require.NoError(t, (<-done).err)

	assert.FileExists(t, outputFile(rig.h.opts.StateDir, testItem, "a#1"), "the replay must not remove the file")
}

func TestAgentStep_AStepWhoseWhenIsFalseStartsNoAgent(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)

	_, err := rig.run(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"prompts/a.md\"\n  when = false\n}\n", "x")

	require.NoError(t, err)
	assert.Empty(t, rig.h.agents.seen())
}

const lintAndFixAgent = `workflow "lint-and-fix" {}

loop "tidy" {
  max   = 3
  until = command.lint.rc == 0

  command "lint" {
    run = ["lint"]
  }

  agent "fix" {
    prompt = "prompts/a.md"
    inputs = { what = command.lint.stdout }
    when   = command.lint.rc != 0
  }
}
`

func TestAgentStep_TheLintAndFixFlowRunsTheAgentOnlyWhileLintFails(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.commands.reply = lintRCs(1, 0)

	_, err := rig.run(t, lintAndFixAgent, writeTo)

	require.NoError(t, err)
	assert.Equal(t, []string{"lint", "lint"}, rig.h.commands.ran())
	assert.Len(t, rig.h.agents.seen(), 1)
}

const replayFlow = `workflow "w" {}
agent "a" {
  prompt = "prompts/a.md"
}
wait "ask" {
  question = "go?"
}
command "after" {
  run = ["after", wait.ask.answer, agent.a.session]
}
`

func TestAgentStep_AReplayDoesNotReadThePromptFileOrRunTheAgentAgain(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	file := writeFlow(t, replayFlow)
	prompt := filepath.Join(filepath.Dir(file), "prompts", "a.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(prompt), 0o750))
	require.NoError(t, os.WriteFile(prompt, []byte("Write to {{ .output_file }}\n"), 0o600))

	interrupted(t, rig.h, file)
	require.NoError(t, os.Remove(prompt))

	next := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "yes") == nil })

	outcome := <-done

	require.NoError(t, outcome.err)
	assert.Len(t, rig.h.agents.seen(), 1, "the agent ran once")
	assert.Equal(t, []string{"after yes sess-1"}, rig.h.commands.ran())
}
