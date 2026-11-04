package flow

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
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

// crashAfterThePhaseEnded runs the flow on an engine that stops after the phase of the agent ended and before the read step is recorded.
// The read step runs git rev-parse HEAD in the item folder. The folder config includes a named pipe, so git blocks on the pipe until a writer opens it:
// the open of the writer returns only when the read is in flight. The phase has ended by then. The engine stops while the read waits.
//
// Every resource has a cleanup registered before it exists, so a failed assertion or a timeout leaves nothing running or open.
// The cleanups run in reverse order: the engine stops first, then the cleanup releases the pipe and waits for the goroutines.
func (r *agentRig) crashAfterThePhaseEnded(t *testing.T, file string) {
	t.Helper()

	r.crashItemAfterThePhaseEnded(t, testItem, file)
}

// crashItemAfterThePhaseEnded is crashAfterThePhaseEnded for one named item.
func (r *agentRig) crashItemAfterThePhaseEnded(t *testing.T, item, file string) {
	t.Helper()

	// The render step reads HEAD before the agent starts, so the pipe goes into the folder config when the agent ends: the first git command that blocks is the read.
	r.crashItemAfterThePhaseEndedArmedBy(t, item, file, func(arm func()) {
		prior := r.h.agents.result

		r.h.agents.result = func(request AgentRequest) agent.Result {
			result := agent.Result{Outcome: agent.OutcomeExited}
			if prior != nil {
				result = prior(request)
			}

			arm()

			return result
		}
	})
}

// crashItemAfterThePhaseEndedArmedBy is crashItemAfterThePhaseEnded with a choice of when the pipe goes into the folder config.
// It hands the arming function to armBy before the engine starts. A git command that runs after the arming blocks on the pipe, so a caller that arms
// from inside the fake agent lets the steps before the phase run free.
func (r *agentRig) crashItemAfterThePhaseEndedArmedBy(t *testing.T, item, file string, armBy func(arm func())) {
	t.Helper()

	gate := filepath.Join(t.TempDir(), "gate")
	require.NoError(t, syscall.Mkfifo(gate, 0o600))

	var (
		routines sync.WaitGroup
		held     = make(chan *os.File, 1)
	)

	// A writer blocked in its open returns when a reader opens the pipe. A non-blocking read open does that and never blocks itself.
	t.Cleanup(func() {
		if reader, err := os.OpenFile(filepath.Clean(gate), os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = reader.Close()
		}

		routines.Wait()

		select {
		case pipe := <-held:
			_ = pipe.Close()
		default:
		}
	})

	armBy(func() { gitIn(t, r.dir, "config", "--local", "include.path", gate) })

	crashed, err := NewEngine(t.Context(), r.h.opts, r.h.deps())
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, crashed.Shutdown()) })

	require.NoError(t, crashed.Launch())

	ran := make(chan error, 1)

	routines.Add(2)

	go func() {
		defer routines.Done()

		_, runErr := crashed.RunFile(item, testProject, file)
		ran <- runErr
	}()

	go func() {
		defer routines.Done()

		pipe, openErr := os.OpenFile(filepath.Clean(gate), os.O_WRONLY, 0)
		assert.NoError(t, openErr)
		held <- pipe
	}()

	var pipe *os.File
	select {
	case pipe = <-held:
	case <-time.After(eventTimeout):
		require.FailNow(t, "the read step did not start")
	}

	require.Equal(t, 1, phaseCount(t, crashed, dbos.WorkflowStatusSuccess), "the phase ended before the read step started")

	require.NoError(t, crashed.Shutdown())

	select {
	case runErr := <-ran:
		require.Error(t, runErr, "the stopped engine ends the run before it records the read step")
	case <-time.After(eventTimeout):
		require.FailNow(t, "the stopped engine did not end the run")
	}

	require.NoError(t, pipe.Close())
	require.NoError(t, os.Remove(gate), "the replay reads HEAD with no gate")
}

const writeTo = "Write to {{ .output_file }}\nFix {{ .what }}.\n"

// aStep is the name of the first agent step that is labeled "a".
const aStep = "agent:a#1"

// agentCallText is a call of the agent builtin with the label "a" on the prompt file prompts/a.md. extra holds more arguments, each followed by a comma.
func agentCallText(extra string) string {
	model := "model = \"m\", "
	if strings.Contains(extra, "model") {
		model = ""
	}

	return "agent(" + model + "prompt = \"prompts/a.md\", " + extra + "name = \"a\")\n"
}

// agentFlow is a workflow file with one agent call.
func agentFlow(extra string) string { return bodyFlow(agentCallText(extra)) }

var exited = agent.Result{Outcome: agent.OutcomeExited, Session: "sess-1", Transcript: "/t.jsonl", Text: "all done"}

func TestAgentStep_RendersThePromptWithTheInputsAndTheOutputFile(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow("inputs = {\"what\": \"the lint\"}, "), writeTo)

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

	_, err := rig.run(t, agentFlow(""), writeTo)

	require.ErrorContains(t, err, "what")
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgentStep_AMissingPromptFileAndABadPromptArgumentAreRefused(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, bodyFlow("agent(model = \"m\", prompt = \"prompts/none.md\", name = \"a\")\n"), writeTo)
	require.ErrorContains(t, err, "none.md")

	rig = newAgentRig(t)
	_, err = rig.run(t, bodyFlow("agent(model = \"m\", prompt = 3 + 0, name = \"a\")\n"), writeTo)
	require.ErrorContains(t, err, "want string")

	rig = newAgentRig(t)
	_, err = rig.run(t, agentFlow("inputs = 3, "), "Write to {{ .output_file }}\n")
	require.ErrorContains(t, err, "inputs")

	rig = newAgentRig(t)
	_, err = rig.run(t, agentFlow(""), "{{ .output_file \n")
	require.ErrorContains(t, err, "a.md")
}

func TestAgentStep_TheModelAndTheTimeoutsGoToTheAgent(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow("model = \"opus\", runner = \"second\", timeout = \"30m\", idle_timeout = \"5m\", "), "Write to {{ .output_file }}\n")

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
	_, err := rig.run(t, agentFlow("timeout = \"so\" + \"on\", "), "x")
	require.ErrorContains(t, err, "not a duration")

	rig = newAgentRig(t)
	_, err = rig.run(t, agentFlow("idle_timeout = \"so\" + \"on\", "), "x")
	require.ErrorContains(t, err, "not a duration")

	rig = newAgentRig(t)
	_, err = rig.run(t, agentFlow("model = 4 + 0, "), "x")
	require.ErrorContains(t, err, "want string")

	rig = newAgentRig(t)
	rig.h.picker = fakeLogins{err: errors.New("login gone")}
	_, err = rig.run(t, agentFlow(""), "Write to {{ .output_file }}\n")
	require.ErrorContains(t, err, "pick the login of item")
}

func TestAgentStep_AnArgumentOfTheWrongTypeFailsTheStep(t *testing.T) {
	t.Parallel()

	for _, arg := range []string{"inputs = \"text\"", "resume = 3", "output_tries = \"x\"", "pin = 3", "outputs = \"verdict\"", "inputs = {1 + 0: 2}", "inputs = {\"a\": len}"} {
		rig := newAgentRig(t)
		_, err := rig.run(t, agentFlow(arg+", "), "x")

		require.Error(t, err, arg)
		assert.Empty(t, rig.h.agents.seen(), arg)
	}
}

func TestAgentStep_AnEmptyResumeStartsFresh(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow("inputs = {\"what\": \"y\"}, resume = \"\", "), writeTo)

	require.NoError(t, err)
	assert.Empty(t, rig.h.agents.seen()[0].Resume)
}

func TestAgentStep_AnOldOutputFileThatCannotBeRemovedFailsTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	require.NoError(t, os.MkdirAll(filepath.Join(outputFile(rig.h.opts.StateDir, testItem, aStep), "inside"), 0o700))

	_, err := rig.run(t, agentFlow(""), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "remove the old output file")
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgentStep_AFolderForTheOutputFileThatCannotBeMadeFailsTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	require.NoError(t, os.MkdirAll(rig.h.opts.StateDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(rig.h.opts.StateDir, outputsFolder), nil, 0o600))

	_, err := rig.run(t, agentFlow(""), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "make the folder of the output file")
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgentStep_TheOutputFileIsInTheStateFolderAndTheWorktreeStaysClean(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"ok"}`, exited)

	_, err := rig.run(t, outputsFlow("1"), "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	status := exec.CommandContext(t.Context(), "git", "status", "--porcelain", "--untracked-files=all")
	status.Dir = rig.dir
	out, err := status.Output()
	require.NoError(t, err)
	assert.Empty(t, string(out), "the worktree has no new file")

	written, err := os.ReadFile(outputFile(rig.h.opts.StateDir, testItem, aStep))
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

func TestOutputFile_TheVerdictPathHoldsOnlyLettersDigitsAndFewMarks(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	path := outputFile(state, "review#5 \"x\"/y", "check it")

	rel, err := filepath.Rel(state, path)
	require.NoError(t, err)
	assert.Regexp(t, `^[A-Za-z0-9._/-]+$`, rel)
	assert.Equal(t, outputsFolder, filepath.Dir(rel), "one folder holds every output file")
}

func TestAgentStep_APinOnAFolderThatIsNotARepositoryFailsTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.trees = &fakeTrees{dir: t.TempDir()}

	_, err := rig.run(t, agentFlow("pin = \"abc\", "), "x")

	require.ErrorContains(t, err, "read HEAD of the item folder")
}

func TestAgentStep_ResumeContinuesTheSessionOfAnEarlierAgent(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, bodyFlow(`first = agent(model = "m", prompt = "prompts/a.md", inputs = {"what": "x"}, name = "first")
agent(model = "m", prompt = "prompts/a.md", inputs = {"what": "y"}, resume = first.session, name = "a")
`), writeTo)
	require.NoError(t, err)

	seen := rig.h.agents.seen()
	require.Len(t, seen, 2)
	assert.Empty(t, seen[0].Resume)
	assert.Equal(t, "sess-1", seen[1].Resume)
}

func TestAgentStep_APinThatIsNotTheFolderHeadIsRefusedWithBothCommits(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow("pin = \"abc123\", "), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "abc123")
	require.ErrorContains(t, err, rig.head)
	assert.Empty(t, rig.h.agents.seen())

	rig = newAgentRig(t)
	rig.reply("", exited)
	_, err = rig.run(t, agentFlow("pin = \""+rig.head+"\", "), "Write to {{ .output_file }}\n")
	require.NoError(t, err)
}

// outputsFlow is an agent call that declares the output "verdict", with the given number of tries, and a command that shows what the call returned.
func outputsFlow(tries string) string {
	return bodyFlow("a = " + agentCallText(`outputs = ["verdict"], output_tries = `+tries+", ") +
		`command(run = ["show", a.verdict, a.text, a.session, a.head_sha, a.transcript])` + "\n")
}

func TestAgentStep_DeclaredOutputsComeFromTheFileAndTheKindOutputsFromTheRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"approve"}`, exited)

	_, err := rig.run(t, outputsFlow("1"), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"show approve all done sess-1 " + rig.head + " /t.jsonl"}, rig.h.commands.ran())
}

func TestAgentStep_AMissingOrIllTypedOutputNamesTheStepAndTheOutput(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]string{
		`{}`:               `the agent did not write the output "verdict"`,
		`{"verdict":3}`:    `the output "verdict" must be a string`,
		`not json`:         `the output file`,
		`{"verdict":null}`: `the agent did not write the output "verdict"`,
	} {
		rig := newAgentRig(t)
		rig.reply(body, exited)

		_, err := rig.run(t, outputsFlow("1"), "Write to {{ .output_file }}\n")

		require.ErrorContains(t, err, want)
		assert.Len(t, rig.h.agents.seen(), 1, "one try in all by default")
	}

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, outputsFlow("1"), "Write to {{ .output_file }}\n")
	require.ErrorContains(t, err, "the agent wrote no output file")
}

func TestAgentStep_OutputTriesRunTheAgentThatManyTimesInAllBeforeFailing(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{}`, exited)

	_, err := rig.run(t, outputsFlow("3"), "Write to {{ .output_file }}\n")

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

	_, err = rig.run(t, outputsFlow("3"), "Write to {{ .output_file }}\n")
	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 2, "the second try passes and ends the tries")
}

func TestAgentStep_AStepWithoutOutputTriesRunsTheAgentOnceInAll(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{}`, exited)

	_, err := rig.run(t, agentFlow("outputs = [\"verdict\"], "), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, `the agent did not write the output "verdict"`)
	assert.Len(t, rig.h.agents.seen(), 1)
}

func TestAgentStep_ABadOutputTriesIsRefused(t *testing.T) {
	t.Parallel()

	for _, tries := range []string{`"two"`, `1 - 1`, `1.5`} {
		rig := newAgentRig(t)
		_, err := rig.run(t, outputsFlow(tries), "x")

		require.ErrorContains(t, err, "output_tries", tries)
		assert.Empty(t, rig.h.agents.seen(), tries)
	}
}

func TestAgentStep_ARequiredSkillThatTheRunDidNotCallFailsTheStepAndNamesIt(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", agent.Result{Outcome: agent.OutcomeExited, Skills: []string{"tdd"}})

	_, err := rig.run(t, agentFlow("require_skills = [\"tdd\", \"review\"], "), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, `the agent did not call the skill "review"`)
	assert.NotContains(t, err.Error(), `"tdd"`)

	rig = newAgentRig(t)
	rig.reply("", agent.Result{Outcome: agent.OutcomeExited, Skills: []string{"tdd", "review"}})

	_, err = rig.run(t, agentFlow("require_skills = [\"tdd\", \"review\"], "), "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	rig = newAgentRig(t)
	_, err = rig.run(t, agentFlow("require_skills = 3 + 0,"), "x")
	require.ErrorContains(t, err, "list of strings")
}

func TestAgentStep_AnAgentThatDoesNotExitCleanlyFailsTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", agent.Result{Outcome: agent.OutcomeTimedOut, Note: "too slow"})

	_, err := rig.run(t, agentFlow(""), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "too slow")
}

func TestAgentStep_AFolderThatCannotBeFoundFailsTheStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.trees = &fakeTrees{err: assert.AnError}

	_, err := rig.run(t, agentFlow(""), "x")
	require.ErrorIs(t, err, assert.AnError)

	rig = newAgentRig(t)
	rig.h.trees = &fakeTrees{dir: t.TempDir()}

	_, err = rig.run(t, agentFlow(""), "Write to {{ .output_file }}\n")
	require.ErrorContains(t, err, "HEAD")
}

func TestAgentStep_AnItemWithNoWorktreeGetsOneBeforeThePinAndTheHeadAreRead(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.trees = &fakeTrees{late: &lateFolder{dir: rig.dir}}
	rig.reply("", exited)

	_, err := rig.run(t, bodyFlow("a = "+agentCallText("pin = \""+rig.head+"\", ")+"command(run = [\"show\", a.head_sha])\n"), "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show " + rig.head}, rig.h.commands.ran())
}

func TestAgentStep_AnEmptyFolderFromEnsureFailsTheStepBeforeTheAgentRuns(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.trees = &fakeTrees{}

	_, err := rig.run(t, agentFlow(""), "x")

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
	require.FileExists(t, outputFile(rig.h.opts.StateDir, testItem, aStep))

	next := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "yes") == nil })
	require.NoError(t, (<-done).err)

	assert.FileExists(t, outputFile(rig.h.opts.StateDir, testItem, aStep), "the replay must not remove the file")
}

func TestAgentStep_AReplayAfterTheAgentEndedButBeforeTheReadReadsTheFileTheAgentLeft(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"ok"}`, exited)

	file := writeFlow(t, afterVerdictFlow)
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("Write to {{ .output_file }}\n"), 0o600))

	rig.crashAfterThePhaseEnded(t, file)
	require.FileExists(t, outputFile(rig.h.opts.StateDir, testItem, aStep))

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"after ok"}, rig.h.commands.ran(), "the replay reads the file that the agent left")
	assert.Len(t, rig.h.agents.seen(), 1, "the agent does not run again")
}

func TestAgentStep_AReplayReadsTheOutputFileThatTheRecordedPromptNames(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"ok"}`, exited)

	file := writeFlow(t, afterVerdictFlow)
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("Write to {{ .output_file }}\n"), 0o600))

	rig.crashAfterThePhaseEnded(t, file)
	require.FileExists(t, outputFile(rig.h.opts.StateDir, testItem, aStep))

	// The name function now gives another path: the engine computes the file in another folder. The record still names the first one.
	next := rig.h.engine(t)
	next.dir = t.TempDir()

	_, err := next.RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"after ok"}, rig.h.commands.ran(), "the replay reads the file that the recorded prompt names")
	assert.NoFileExists(t, outputFile(next.dir, testItem, aStep), "the replay does not use the path the name function gives today")
}

var afterVerdictFlow = bodyFlow("a = " + agentCallText(`outputs = ["verdict"], `) + `command(run = ["after", a.verdict])` + "\n")

var lintAndFixAgent = bodyFlow(`for _ in range(3):
    lint = command(run = ["lint"], name = "lint")
    if lint.rc == 0:
        break
    agent(model = "m", prompt = "prompts/a.md", inputs = {"what": lint.stdout}, name = "fix")
`)

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

var replayFlow = bodyFlow(`a = agent(model = "m", prompt = "prompts/a.md", name = "a")
ask = wait("go?", name = "ask")
command(run = ["after", ask.answer, a.session])
`)

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

var forbidFlow = bodyFlow(`a = agent(model = "m", prompt = "prompts/a.md", forbid_commands = ["bulk-staging", "local-delete"], name = "a")
command(run = ["show", a.violations])
`)

func TestAgentStep_TheKindsOfForbidCommandsGoToTheAgentRequest(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, forbidFlow, "x")

	require.NoError(t, err)
	require.Len(t, rig.h.agents.seen(), 1)
	assert.Equal(t, []model.FindingKind{model.FindingBulkStaging, model.FindingLocalDelete}, rig.h.agents.seen()[0].Forbid)

	rig = newAgentRig(t)
	rig.reply("", exited)

	_, err = rig.run(t, agentFlow(""), "x")

	require.NoError(t, err)
	assert.Empty(t, rig.h.agents.seen()[0].Forbid)
}

func TestAgentStep_EditsFalseGoesToTheAgentRequestAndTheDefaultAllowsEdits(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow("edits = False, "), "x")

	require.NoError(t, err)
	require.Len(t, rig.h.agents.seen(), 1)
	assert.True(t, rig.h.agents.seen()[0].NoEdits)

	rig = newAgentRig(t)
	rig.reply("", exited)

	_, err = rig.run(t, agentFlow("edits = True, "), "x")

	require.NoError(t, err)
	assert.False(t, rig.h.agents.seen()[0].NoEdits)

	rig = newAgentRig(t)
	rig.reply("", exited)

	_, err = rig.run(t, agentFlow(""), "x")

	require.NoError(t, err)
	assert.False(t, rig.h.agents.seen()[0].NoEdits)
}

func TestAgentStep_ANonBoolEditsFailsTheStepAndNamesTheKey(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	_, err := rig.run(t, agentFlow(`edits = "no", `), "x")

	require.ErrorContains(t, err, "edits")
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgentStep_AViolationIsAnOutputAndTheStepStillSucceeds(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	hit := exited
	hit.Violations = []string{"bulk-staging: git add -A"}
	rig.reply("", hit)

	_, err := rig.run(t, forbidFlow, "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show bulk-staging: git add -A"}, rig.h.commands.ran())
}

var capFlow = bodyFlow(`a = agent(model = "m", prompt = "prompts/a.md", name = "a")
command(run = ["show", "[" + a.stop + "]"])
`)

func TestAgentStep_ARunThatACapStoppedIsNotAFailureAndTheOutputStopNamesTheCap(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		outcome agent.Outcome
		show    string
	}{
		{agent.OutcomeTurnCap, "show [turns]"},
		{agent.OutcomeBudgetCap, "show [budget]"},
		{agent.OutcomeExited, "show []"},
	} {
		rig := newAgentRig(t)
		capped := exited
		capped.Outcome = tc.outcome
		rig.reply("", capped)

		_, err := rig.run(t, capFlow, "x")

		require.NoError(t, err, tc.outcome)
		assert.Equal(t, []string{tc.show}, rig.h.commands.ran(), tc.outcome)
	}
}

func TestAgentStep_AReplayAfterTheAgentEndedKeepsTheViolationItRecorded(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	hit := exited
	hit.Violations = []string{"bulk-staging: git add -A"}
	rig.reply("", hit)

	file := writeFlow(t, forbidFlow)
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("Do the work\n"), 0o600))

	rig.crashAfterThePhaseEnded(t, file)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"show bulk-staging: git add -A"}, rig.h.commands.ran(), "the replay reads the violation from the record")
	assert.Len(t, rig.h.agents.seen(), 1, "the agent does not run again, so no transcript is read again")
}

func TestAgentStep_ACleanRunHasEmptyViolations(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, forbidFlow, "x")

	require.NoError(t, err)
	assert.Equal(t, []string{"show "}, rig.h.commands.ran())
}

func TestAgentStep_TheUntilOfALoopReadsTheViolationsOfAnAgent(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	calls := 0
	rig.h.agents.result = func(AgentRequest) agent.Result {
		calls++

		out := exited
		if calls == 1 {
			out.Violations = []string{"local-delete: rm -rf $d", "bulk-staging: git add ."}
		}

		return out
	}

	_, err := rig.run(t, bodyFlow(`for _ in range(3):
    a = agent(model = "m", prompt = "prompts/a.md", forbid_commands = ["bulk-staging", "local-delete"], name = "a")
    if a.violations == "":
        break
`), "x")

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 2, "the loop ends in the round that has no violation")
}

func TestAgentStep_ProtectPathsGoToTheAgentRequestBesideTheProtectedPathKind(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`forbid_commands = ["protected-path"], protect_paths = ["/own/checkout", "rel/dir"], `), "x")

	require.NoError(t, err)
	require.Len(t, rig.h.agents.seen(), 1)
	assert.Equal(t, []model.FindingKind{model.FindingProtected}, rig.h.agents.seen()[0].Forbid)
	assert.Equal(t, []string{"/own/checkout", "rel/dir"}, rig.h.agents.seen()[0].Protect)
}

func TestAgentStep_AKindThatNeedsPathsWithNoPathsIsRefusedWhenTheStepRuns(t *testing.T) {
	t.Parallel()

	for _, extra := range []string{
		// The check refuses a literal list before the run, so each case builds a list that the check does not read.
		`forbid_commands = [] + ["protected-path"], `,
		`forbid_commands = [] + ["protected-path"], protect_paths = [] + [], `,
		`forbid_commands = [] + ["protected-path"], protect_paths = [] + [""], `,
		`forbid_commands = [] + ["bulk-staging"], protect_paths = [] + ["/own/checkout"], `,
		`protect_paths = [] + ["/own/checkout"], `,
		`forbid_commands = [] + ["protected-path"], protect_paths = [] + [3], `,
		`forbid_commands = [] + ["protected-path"], protect_paths = 3 + 0, `,
	} {
		t.Run(extra, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)

			_, err := rig.run(t, agentFlow(extra), "x")

			require.ErrorContains(t, err, "protect_paths")
			assert.Empty(t, rig.h.agents.seen())
		})
	}
}

func TestAgentStep_AForbidCommandsKindThatTheFileDoesNotKnowIsRefusedWhenTheStepRuns(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)

	_, err := rig.run(t, agentFlow("forbid_commands = [] + [\"not-a-kind\"], "), "x")

	require.ErrorContains(t, err, `forbid_commands has the unknown kind "not-a-kind". Use one of: `+strings.Join(model.FindingKindNames(), ", "))
	assert.Empty(t, rig.h.agents.seen())

	rig = newAgentRig(t)

	_, err = rig.run(t, agentFlow("forbid_commands = [] + [3], "), "x")

	require.ErrorContains(t, err, "forbid_commands has the unknown kind 3. Use one of: "+strings.Join(model.FindingKindNames(), ", "))
	assert.Empty(t, rig.h.agents.seen())

	rig = newAgentRig(t)

	_, err = rig.run(t, agentFlow("forbid_commands = 3 + 0,"), "x")

	require.ErrorContains(t, err, "list of strings")
	assert.Empty(t, rig.h.agents.seen())
}
