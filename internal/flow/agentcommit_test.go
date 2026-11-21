package flow

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	cloneName   = "Clone Name"
	cloneEmail  = "clone@example.com"
	goodOutput  = `{"commit_subject":"fix(a): add a file","commit_body":"The file holds the fix."}`
	attribution = "Co-Authored-By: Claude <noreply@anthropic.com>"
)

// commitFlow is an agent call that may edit, and a command that shows the head and the committed output of the call.
func commitFlow(extra string) string {
	return bodyFlow("a = " + agentCallText(extra) + `command(run = ["show", a.head_sha, str(a.committed)])` + "\n")
}

// commitRig is an agent rig whose folder has the git identity of the engine.
func commitRig(t *testing.T) *agentRig {
	t.Helper()

	rig := newAgentRig(t)
	gitIn(t, rig.dir, "config", "user.name", cloneName)
	gitIn(t, rig.dir, "config", "user.email", cloneEmail)

	return rig
}

// writes makes the fake agent write each file in the folder, and the output file with output when output is not empty.
func (r *agentRig) writes(files map[string]string, output string) {
	r.h.agents.result = func(request AgentRequest) agent.Result {
		for name, body := range files {
			r.put(filepath.Join(r.dir, name), body)
		}

		if output != "" {
			r.put(outputPath(request.Prompt), output)
		}

		return exited
	}
}

func (r *agentRig) tip(t *testing.T) string {
	t.Helper()

	return gitIn(t, r.dir, "rev-parse", "HEAD")
}

func TestEngineCommit_AnEditingStepsChangedFilesAreCommittedByTheEngineWithTheTypedSubjectAndBody(t *testing.T) {
	t.Parallel()

	rig := commitRig(t)
	rig.writes(map[string]string{"a.txt": "one"}, goodOutput)

	_, err := rig.run(t, commitFlow(""), "Write to {{ .output_file }}\n")

	require.NoError(t, err)

	head := rig.tip(t)
	assert.NotEqual(t, rig.head, head, "the engine made a commit")
	assert.Equal(t, rig.head, gitIn(t, rig.dir, "rev-parse", "HEAD^"))
	assert.Equal(t, "fix(a): add a file", gitIn(t, rig.dir, "log", "-1", "--format=%s"))
	assert.Equal(t, "The file holds the fix.", gitIn(t, rig.dir, "log", "-1", "--format=%b"))
	assert.Equal(t, []string{"show " + head + " True"}, rig.h.commands.ran())
	assert.Empty(t, gitIn(t, rig.dir, "status", "--porcelain"))
}

func TestEngineCommit_OnlyTheStepsChangedPathsAreStagedAndNeverABulkAdd(t *testing.T) {
	t.Parallel()

	rig := commitRig(t)
	rig.put(filepath.Join(rig.dir, ".gitignore"), "*.log\n")
	rig.put(filepath.Join(rig.dir, "gone.txt"), "tracked")
	gitIn(t, rig.dir, "add", ".gitignore", "gone.txt")
	gitIn(t, rig.dir, "commit", "-q", "-m", "tracked")

	start := rig.tip(t)
	rig.put(filepath.Join(rig.dir, "wxld1.txt"), "a file the agent did not touch")

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "w*ld[1].txt"), "a name that a glob would widen")
		rig.put(filepath.Join(rig.dir, "debug.log"), "ignored")
		rig.put(outputPath(request.Prompt), goodOutput)
		assert.NoError(t, os.Remove(filepath.Join(rig.dir, "gone.txt")))

		return exited
	}

	_, err := rig.run(t, commitFlow(""), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, "D\tgone.txt\nA\tw*ld[1].txt", gitIn(t, rig.dir, "diff", "--name-status", start, "HEAD"))
	assert.Equal(t, "?? wxld1.txt", strings.TrimSpace(gitIn(t, rig.dir, "status", "--porcelain")), "the untracked file and the ignored file stay out")
}

func TestEngineCommit_TheCommitCarriesTheEngineIdentityAndNoTrailer(t *testing.T) {
	t.Parallel()

	rig := commitRig(t)
	rig.writes(map[string]string{"a.txt": "one"}, goodOutput)

	_, err := rig.run(t, commitFlow(""), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, EngineName+" "+EngineEmail+" "+EngineName+" "+EngineEmail, gitIn(t, rig.dir, "log", "-1", "--format=%an %ae %cn %ce"))
	assert.Equal(t, "fix(a): add a file\n\nThe file holds the fix.", gitIn(t, rig.dir, "log", "-1", "--format=%B"), "the message is the subject and the body and nothing else")
}

func TestEngineCommit_AMessageThatGitReadsATrailerFromIsRefusedAsAnOutputProblem(t *testing.T) {
	t.Parallel()

	rig := commitRig(t)
	calls := 0
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		calls++

		body := "The file holds the fix.\\n\\n" + attribution
		if calls == 2 {
			body = "The file holds the fix."
		}

		rig.put(filepath.Join(rig.dir, "a.txt"), "one")
		rig.put(outputPath(request.Prompt), `{"commit_subject":"fix(a): add a file","commit_body":"`+body+`"}`)

		return exited
	}

	_, err := rig.run(t, commitFlow("output_tries = 2, "), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, 2, calls, "the refusal ran the agent again inside the output tries")
	assert.Equal(t, "fix(a): add a file\n\nThe file holds the fix.", gitIn(t, rig.dir, "log", "-1", "--format=%B"))
	assert.Equal(t, "2", gitIn(t, rig.dir, "rev-list", "--count", "HEAD"), "one commit besides the first: the refused message made none")
}

func TestEngineCommit_ARefusedTrailerWithNoOutputTriesFailsTheStepAndCommitsNothing(t *testing.T) {
	t.Parallel()

	rig := commitRig(t)
	rig.writes(map[string]string{"a.txt": "one"}, `{"commit_subject":"fix(a): x","commit_body":"Body.\n\n`+attribution+`"}`)

	_, err := rig.run(t, commitFlow(""), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "the commit body must not end with a trailer such as Co-Authored-By")
	assert.Equal(t, rig.head, rig.tip(t), "no commit")
}

func TestEngineCommit_AReplayAfterTheCommitBeforeTheRecordWritesNoSecondCommit(t *testing.T) {
	t.Parallel()

	a, outputs := commitArgs(t)
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("one"), 0o600))

	first, err := a.commitStep(t.Context(), outputs)
	require.NoError(t, err)
	assert.Equal(t, true, first.Outputs["committed"])

	head := gitIn(t, a.dir, "rev-parse", "HEAD")
	second, err := a.commitStep(t.Context(), outputs)

	require.NoError(t, err)
	assert.Equal(t, head, first.Outputs["head_sha"])
	assert.Equal(t, head, second.Outputs["head_sha"], "the replay returns the engine commit as the head")
	assert.Equal(t, true, second.Outputs["committed"], "and says that the step committed")
	assert.Equal(t, head, gitIn(t, a.dir, "rev-parse", "HEAD"))
	assert.Equal(t, "2", gitIn(t, a.dir, "rev-list", "--count", "HEAD"))
}

func TestEngineCommit_AHeadThatTheAgentCommittedIsNotTakenForTheCommitOfTheEngine(t *testing.T) {
	t.Parallel()

	a, outputs := commitArgs(t)
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("one"), 0o600))
	gitIn(t, a.dir, "add", "a.txt")
	gitIn(t, a.dir, "commit", "-q", "-m", "an agent commit")

	second, err := a.commitStep(t.Context(), outputs)

	require.NoError(t, err)
	assert.Empty(t, second.Outputs, "a commit with another subject is not the commit of this step, and nothing is left to commit")
}

func TestEngineCommit_AFailingReadOfTheLastCommitFailsTheStep(t *testing.T) {
	t.Parallel()

	a, outputs := commitArgs(t)
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("one"), 0o600))
	_, err := a.commitStep(t.Context(), outputs)
	require.NoError(t, err)

	inner := a.git
	a.git = failing(func(args []string) model.CommandResult { return inner.Run(t.Context(), args) }, "log")

	_, err = a.commitStep(t.Context(), outputs)

	require.ErrorContains(t, err, "read the last commit of the item folder")
}

// trackedDirty is a folder with the tracked file pre.txt, which was edited before the step, and the start mark of it.
func trackedDirty(t *testing.T) (agentArgs, map[string]any) {
	t.Helper()

	dir, _ := gitTree(t)
	gitIn(t, dir, "config", "user.name", cloneName)
	gitIn(t, dir, "config", "user.email", cloneEmail)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pre.txt"), []byte("tracked"), 0o600))
	gitIn(t, dir, "add", "pre.txt")
	gitIn(t, dir, "commit", "-q", "-m", "tracked")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pre.txt"), []byte("edited before the step"), 0o600))

	a := agentArgs{dir: dir, git: engineGit(dir), edits: true, file: filepath.Join(t.TempDir(), "output.json")}

	var err error

	a.start, err = takeMark(t.Context(), a.git)
	require.NoError(t, err)

	a.startHead = a.start.Head

	return a, map[string]any{commitSubjectField: "fix(a): add a file", commitBodyField: "", "stop": "", "head_sha": a.start.Head}
}

func TestEngineCommit_ATrackedFileThatWasChangedAtTheStartIsNotCommittedUnlessTheStepChangedItFurther(t *testing.T) {
	t.Parallel()

	a, outputs := trackedDirty(t)
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("one"), 0o600))

	_, err := a.commitStep(t.Context(), outputs)

	require.NoError(t, err)
	assert.Equal(t, "A\ta.txt", gitIn(t, a.dir, "show", "--name-status", "--format=", "HEAD"))
	assert.Equal(t, "M pre.txt", strings.TrimSpace(gitIn(t, a.dir, "status", "--porcelain")), "the edit of before the step stays in the folder")

	b, outputs := trackedDirty(t)
	require.NoError(t, os.WriteFile(filepath.Join(b.dir, "pre.txt"), []byte("edited again by the step"), 0o600))

	_, err = b.commitStep(t.Context(), outputs)

	require.NoError(t, err)
	assert.Equal(t, "M\tpre.txt", gitIn(t, b.dir, "show", "--name-status", "--format=", "HEAD"), "a file that the step changed further is the step's")
}

func TestEngineCommit_AFileThatWasStagedAtTheStartAndIsNotTheStepsStaysOutOfTheCommit(t *testing.T) {
	t.Parallel()

	a, outputs := trackedDirty(t)
	gitIn(t, a.dir, "add", "pre.txt")

	var err error

	a.start, err = takeMark(t.Context(), a.git)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("one"), 0o600))

	_, err = a.commitStep(t.Context(), outputs)

	require.NoError(t, err)
	assert.Equal(t, "A\ta.txt", gitIn(t, a.dir, "show", "--name-status", "--format=", "HEAD"))
	assert.Equal(t, "M  pre.txt", strings.TrimRight(gitIn(t, a.dir, "status", "--porcelain"), "\n"), "the staged file stays staged")
}

func TestEngineCommit_AFileThatWasDeletedAtTheStartAndStaysDeletedIsNotTheSteps(t *testing.T) {
	t.Parallel()

	a, outputs := trackedDirty(t)
	require.NoError(t, os.Remove(filepath.Join(a.dir, "pre.txt")))

	var err error

	a.start, err = takeMark(t.Context(), a.git)
	require.NoError(t, err)
	assert.Equal(t, "gone", a.start.Dirty["pre.txt"])
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("one"), 0o600))

	_, err = a.commitStep(t.Context(), outputs)

	require.NoError(t, err)
	assert.Equal(t, "A\ta.txt", gitIn(t, a.dir, "show", "--name-status", "--format=", "HEAD"))
}

func TestEngineCommit_AGitCallThatFailsInTheStartMarkFailsIt(t *testing.T) {
	t.Parallel()

	for step, want := range map[string]string{
		"diff":        "list the tracked files that are changed at the start",
		"hash-object": "hash the file pre.txt",
	} {
		a, _ := trackedDirty(t)
		inner := a.git

		_, err := takeMark(t.Context(), failing(func(args []string) model.CommandResult { return inner.Run(t.Context(), args) }, step))

		require.ErrorContains(t, err, want, step)
	}
}

func TestEngineCommit_AGitCallThatFailsWhileTheStepIsComparedWithTheStartFailsTheStep(t *testing.T) {
	t.Parallel()

	a, outputs := trackedDirty(t)
	inner := a.git
	run := func(args []string) model.CommandResult { return inner.Run(t.Context(), args) }
	a.git = failing(run, "hash-object")

	_, err := a.commitStep(t.Context(), outputs)

	require.ErrorContains(t, err, "hash the file pre.txt")

	_, err = dirtyTracked(t.Context(), failing(run, "--show-toplevel"))

	require.ErrorContains(t, err, "find the root of the item folder")
}

func TestEngineCommit_NoSettingOfTheAgentInTheSameStepChangesTheIdentityOfTheCommit(t *testing.T) {
	t.Parallel()

	rig := commitRig(t)
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		gitIn(t, rig.dir, "config", "user.email", "evil@example.com")
		gitIn(t, rig.dir, "config", "user.name", "Evil")
		rig.put(filepath.Join(rig.dir, "a.txt"), "one")
		rig.put(outputPath(request.Prompt), goodOutput)

		return exited
	}

	_, err := rig.run(t, commitFlow(""), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, EngineEmail+" "+EngineName+" "+EngineEmail+" "+EngineName, gitIn(t, rig.dir, "log", "-1", "--format=%ae %an %ce %cn"))
}

func TestEngineCommit_ASettingOfTheAgentInAnEarlierStepDoesNotChangeTheIdentityOfALaterCommit(t *testing.T) {
	t.Parallel()

	rig := commitRig(t)
	calls := 0
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		calls++
		if calls == 1 {
			gitIn(t, rig.dir, "config", "user.email", "evil@example.com")
			gitIn(t, rig.dir, "config", "user.name", "Evil")

			return exited
		}

		rig.put(filepath.Join(rig.dir, "a.txt"), "one")
		rig.put(outputPath(request.Prompt), goodOutput)

		return exited
	}

	text := "x = agent(model = \"m\", prompt = \"prompts/a.md\", name = \"a\")\n" +
		"y = agent(model = \"m\", prompt = \"prompts/a.md\", name = \"b\")\n" +
		"command([\"show\", y.head_sha])\n"

	_, err := rig.run(t, bodyFlow(text), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, EngineEmail+" "+EngineName+" "+EngineEmail+" "+EngineName, gitIn(t, rig.dir, "log", "-1", "--format=%ae %an %ce %cn"))
}

func TestEngineCommit_AReplayAfterAnAgentCommitAndTheCrashBeforeTheRecordReturnsTheEngineCommit(t *testing.T) {
	t.Parallel()

	a, outputs := commitArgs(t)
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "b.txt"), []byte("agent"), 0o600))
	gitIn(t, a.dir, "add", "b.txt")
	gitIn(t, a.dir, "commit", "-q", "-m", "agent commit")

	outputs["head_sha"], outputs["committed"] = gitIn(t, a.dir, "rev-parse", "HEAD"), true
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("left over"), 0o600))

	_, err := a.commitStep(t.Context(), outputs)
	require.NoError(t, err)

	engineHead := gitIn(t, a.dir, "rev-parse", "HEAD")
	second, err := a.commitStep(t.Context(), outputs)

	require.NoError(t, err)
	assert.Equal(t, engineHead, second.Outputs["head_sha"], "the replay anchors on the head that the agent left, not on the start head")
	assert.Equal(t, true, second.Outputs["committed"])
	assert.Equal(t, "3", gitIn(t, a.dir, "rev-list", "--count", "HEAD"), "no third commit")
}

func TestEngineCommit_AChangeOfTheModeAloneOrOfTheTargetOfALinkAtTheStartIsNotTheStepsAndAFurtherChangeIs(t *testing.T) {
	t.Parallel()

	a, outputs := trackedDirty(t)
	require.NoError(t, exectest.Write(filepath.Join(a.dir, "run.sh"), "#!/bin/sh\n"))
	require.NoError(t, os.Symlink("pre.txt", filepath.Join(a.dir, "link")))
	gitIn(t, a.dir, "add", "run.sh", "link")
	gitIn(t, a.dir, "update-index", "--chmod=-x", "run.sh")
	gitIn(t, a.dir, "commit", "-q", "-m", "more")
	require.NoError(t, os.Remove(filepath.Join(a.dir, "link")))
	require.NoError(t, os.Symlink("elsewhere", filepath.Join(a.dir, "link")))

	var err error

	a.start, err = takeMark(t.Context(), a.git)
	require.NoError(t, err)
	assert.Contains(t, a.start.Dirty["link"], "link:elsewhere")
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("one"), 0o600))

	_, err = a.commitStep(t.Context(), outputs)

	require.NoError(t, err)
	assert.Equal(t, "A\ta.txt", gitIn(t, a.dir, "show", "--name-status", "--format=", "HEAD"), "the mode and the link stay out")

	require.NoError(t, os.Remove(filepath.Join(a.dir, "link")))
	require.NoError(t, os.Symlink("another", filepath.Join(a.dir, "link")))

	outputs["head_sha"] = gitIn(t, a.dir, "rev-parse", "HEAD")
	_, err = a.commitStep(t.Context(), outputs)

	require.NoError(t, err)
	assert.Equal(t, "M\tlink", gitIn(t, a.dir, "show", "--name-status", "--format=", "HEAD"), "a further change of the target is the step's, and the mode stays out")
}

func TestEngineCommit_AStepThatOnlyFlipsTheExecuteBitOfAFileThatWasChangedAtTheStartCommitsIt(t *testing.T) {
	t.Parallel()

	a, outputs := trackedDirty(t)
	require.NoError(t, os.Remove(filepath.Join(a.dir, "pre.txt")))
	require.NoError(t, exectest.Write(filepath.Join(a.dir, "pre.txt"), "edited before the step"))

	_, err := a.commitStep(t.Context(), outputs)

	require.NoError(t, err)
	assert.Equal(t, "M\tpre.txt", gitIn(t, a.dir, "show", "--name-status", "--format=", "HEAD"), "the flip of the bit is the step's, whatever the content")
}

func TestEngineCommit_AStepThatChangedNothingMakesNoCommit(t *testing.T) {
	t.Parallel()

	rig := commitRig(t)
	rig.writes(nil, goodOutput)

	_, err := rig.run(t, commitFlow(""), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, rig.head, rig.tip(t))
	assert.Equal(t, []string{"show " + rig.head + " False"}, rig.h.commands.ran())
}

func TestEngineCommit_AStepThatWritesNoSubjectMakesNoCommitAndKeepsTheChanges(t *testing.T) {
	t.Parallel()

	for name, output := range map[string]string{"no output file": "", "an output file with no message": `{}`} {
		rig := commitRig(t)
		rig.writes(map[string]string{"a.txt": "one"}, output)

		_, err := rig.run(t, commitFlow(""), "Write to {{ .output_file }}\n")

		require.NoError(t, err, name)
		assert.Equal(t, rig.head, rig.tip(t), name)
		assert.Equal(t, "?? a.txt", gitIn(t, rig.dir, "status", "--porcelain"), name)
	}
}

func TestEngineCommit_AStepThatDoesNotEditOrHasNoRepositoryNeverCommits(t *testing.T) {
	t.Parallel()

	rig := commitRig(t)
	rig.writes(map[string]string{"a.txt": "one"}, goodOutput)

	_, err := rig.run(t, commitFlow("edits = False, "), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, rig.head, rig.tip(t))
	assert.False(t, agentArgs{edits: true, plain: true}.commits())
	assert.Empty(t, agentArgs{edits: true, plain: true, declared: nil}.wanted())
}

func TestEngineCommit_AStepThatDeclaresACommitFieldListsItOnce(t *testing.T) {
	t.Parallel()

	a := agentArgs{edits: true, declared: []string{"verdict", commitSubjectField}}

	assert.Equal(t, []string{"verdict", commitSubjectField, commitBodyField}, a.wanted())
}

func TestEngineCommit_ABadMessageFieldIsAnOutputProblem(t *testing.T) {
	t.Parallel()

	for output, want := range map[string]string{
		`{"commit_subject":""}`:                        "must be one line of text that is not empty",
		`{"commit_subject":"  "}`:                      "must be one line of text that is not empty",
		`{"commit_subject":"one\ntwo"}`:                "must be one line of text that is not empty",
		`{"commit_body":"text"}`:                       "commit_body and no commit_subject",
		`{"commit_subject":7}`:                         "as text",
		`[1]`:                                          "must be a JSON object",
		`{"commit_subject":"fix: a","commit_body":[]}`: "as text",
	} {
		a, outputs := commitArgs(t)
		require.NoError(t, os.WriteFile(a.file, []byte(output), 0o600))

		problem, err := a.readCommit(t.Context(), outputs)

		require.NoError(t, err)
		assert.Contains(t, problem, want, output)
	}
}

func TestEngineCommit_AStepThatACapStoppedReadsNoMessage(t *testing.T) {
	t.Parallel()

	a, _ := commitArgs(t)
	require.NoError(t, os.WriteFile(a.file, []byte(goodOutput), 0o600))
	outputs := map[string]any{"stop": "turns"}

	problem, err := a.readCommit(t.Context(), outputs)

	require.NoError(t, err)
	assert.Empty(t, problem)
	assert.NotContains(t, outputs, commitSubjectField)
}

func TestEngineCommit_ASubjectWithNoBodyIsAMessageOfOneLine(t *testing.T) {
	t.Parallel()

	a, _ := commitArgs(t)
	require.NoError(t, os.WriteFile(a.file, []byte(`{"commit_subject":"fix: a"}`), 0o600))
	outputs := map[string]any{"stop": ""}

	problem, err := a.readCommit(t.Context(), outputs)

	require.NoError(t, err)
	assert.Empty(t, problem)
	assert.Equal(t, "fix: a", outputs[commitSubjectField])
	assert.Empty(t, outputs[commitBodyField])

	file, err := a.writeMessage("fix: a", "")
	require.NoError(t, err)

	text, err := fs.ReadFile(os.DirFS(filepath.Dir(file)), filepath.Base(file))
	require.NoError(t, err)
	assert.Equal(t, "fix: a\n", string(text))
}

func TestEngineCommit_ARenamedPathIsOneChangeAndItsOldPathIsNone(t *testing.T) {
	t.Parallel()

	got := parseStatus("R  new.txt\x00old.txt\x00?? b.txt\x00 M c.txt\x00")

	assert.Equal(t, []change{{"R ", "new.txt"}, {"??", "b.txt"}, {" M", "c.txt"}}, got)
}

// commitArgs is the arguments of an editing call over a folder with the engine identity and a started mark, and the outputs of a call that wrote a message.
func commitArgs(t *testing.T) (agentArgs, map[string]any) {
	t.Helper()

	dir, head := gitTree(t)
	gitIn(t, dir, "config", "user.name", cloneName)
	gitIn(t, dir, "config", "user.email", cloneEmail)

	state := t.TempDir()
	a := agentArgs{dir: dir, git: engineGit(dir), edits: true, startHead: head, file: filepath.Join(state, "output.json")}

	var err error

	a.start, err = takeMark(t.Context(), a.git)
	require.NoError(t, err)

	return a, map[string]any{commitSubjectField: "fix(a): add a file", commitBodyField: "The file holds the fix.", "stop": "", "head_sha": a.start.Head}
}

// failing is a git runner for a folder: it answers every call as the real git would, except the calls whose first word is in fails, which exit 1.
func failing(inner scriptRunner, fails ...string) scriptRunner {
	return func(args []string) model.CommandResult {
		for _, word := range fails {
			if slices.Contains(args, word) {
				return model.CommandResult{ExitCode: 1, Stderr: "refused " + word}
			}
		}

		return inner(args)
	}
}

func TestEngineCommit_AGitCallThatFailsFailsTheStepWithTheWordsOfTheCall(t *testing.T) {
	t.Parallel()

	for step, want := range map[string]string{
		"status":              "list the files that the step changed",
		"--show-toplevel":     "find the root of the item folder",
		"--pathspec-file-nul": "stage the files that the step changed",
		"commit":              "commit the files that the step changed",
		"HEAD":                "read HEAD of the item folder",
	} {
		a, outputs := commitArgs(t)
		require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("one"), 0o600))

		inner := a.git
		a.git = failing(func(args []string) model.CommandResult { return inner.Run(t.Context(), args) }, step)

		_, err := a.commitStep(t.Context(), outputs)

		require.ErrorContains(t, err, want, step)
	}
}

func TestEngineCommit_AFolderWithNoIdentityIsCommittedWithTheIdentityOfTheEngine(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	a, outputs := commitArgs(t)
	gitIn(t, a.dir, "config", "--unset", "user.name")
	gitIn(t, a.dir, "config", "--unset", "user.email")
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("one"), 0o600))

	_, err := a.commitStep(t.Context(), outputs)

	require.NoError(t, err)
	assert.Equal(t, EngineEmail, gitIn(t, a.dir, "log", "-1", "--format=%ae"))
}

func TestEngineCommit_ACommitThatFailsFailsTheStepAndLeavesTheChangesInTheFolder(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	branch := gitIn(t, rig.dir, "symbolic-ref", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(rig.dir, ".git", filepath.FromSlash(branch)+".lock"), nil, 0o600), "a lock on the branch makes the commit fail")
	rig.writes(map[string]string{"a.txt": "one"}, goodOutput)

	_, err := rig.run(t, commitFlow(""), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "commit the files that the step changed")
	assert.Equal(t, rig.head, rig.tip(t))
	assert.Equal(t, "A  a.txt", gitIn(t, rig.dir, "status", "--porcelain"), "the files are staged and not committed")
}

func TestEngineCommit_AFolderForTheMessageThatCannotBeWrittenFailsTheStep(t *testing.T) {
	t.Parallel()

	a, outputs := commitArgs(t)
	a.file = filepath.Join(t.TempDir(), "missing", "output.json")
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("one"), 0o600))

	_, err := a.commitStep(t.Context(), outputs)
	require.ErrorContains(t, err, "write the list of paths to commit")

	_, err = a.trailerProblem(t.Context(), "fix: a", "")
	require.ErrorContains(t, err, "write the commit message")
}

func TestEngineCommit_ThePathsOfTheListAreWrittenAndTheMessageFailureNamesTheMessage(t *testing.T) {
	t.Parallel()

	a, _ := commitArgs(t)
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "a.txt"), []byte("one"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(filepath.Dir(a.file), commitMessageFile), 0o700))

	_, err := a.commitPaths(t.Context(), []string{"a.txt"}, "fix: a", "")

	require.ErrorContains(t, err, "write the commit message")
}

func TestEngineCommit_AnInterpretTrailersThatFailsFailsTheRead(t *testing.T) {
	t.Parallel()

	a, outputs := commitArgs(t)
	require.NoError(t, os.WriteFile(a.file, []byte(goodOutput), 0o600))

	inner := a.git
	a.git = failing(func(args []string) model.CommandResult { return inner.Run(t.Context(), args) }, "interpret-trailers")

	_, err := a.readCommit(t.Context(), outputs)

	require.ErrorContains(t, err, "check the commit message for a trailer")
}

func TestEngineCommit_GitDecidesWhichMessagesHoldATrailer(t *testing.T) {
	t.Parallel()

	a, _ := commitArgs(t)

	for _, one := range messageCases(t) {
		subject, body, _ := strings.Cut(one.text, "\n\n")

		problem, err := a.trailerProblem(t.Context(), subject, body)

		require.NoError(t, err)
		assert.Equal(t, one.refuse, problem != "", "git %s a trailer in %q", map[bool]string{true: "reads", false: "reads no"}[one.refuse], one.text)
	}
}

// messageCase is a commit message and git's own verdict on it.
type messageCase struct {
	text   string
	refuse bool
}

// messageCases are a fixed table of messages and, for each trailer key in each place of a body, the message with git's own verdict.
// The verdict of a generated message is git interpret-trailers --parse, never a pattern of this package.
func messageCases(t *testing.T) []messageCase {
	t.Helper()

	cases := []messageCase{
		{"fix(x): a subject\n\nBody text.\n\n" + attribution, true},
		{"fix(x): a subject\n\nBody text.\n\nSigned-off-by: R <r@x.y>", true},
		{"fix(x): a subject\n\nBody text.\n\nReviewed-by: R <r@x.y>\na prose line in the block\n" + attribution, false},
		{"fix(x): a subject\n\nThe parser rejects empty input.", false},
	}

	dir, _ := gitTree(t)
	file := filepath.Join(t.TempDir(), "message.txt")

	for _, key := range []string{"Co-Authored-By", "co-authored-by", "CO-AUTHORED-BY", "Signed-off-by", "Reviewed-by", "Co-Authored-By "} {
		trailer := key + ": Claude <noreply@anthropic.com>"

		for _, body := range []string{
			trailer, "One.\n\n" + trailer, "One.\nTwo.\n\n" + trailer, "One.\n\nTwo.\n\n" + trailer, "One.\n\n" + trailer + "\n\nTwo.", "One.\n\n" + trailer + "\nprose",
		} {
			text := "fix(x): a subject\n\n" + body + "\n"
			require.NoError(t, os.WriteFile(file, []byte(text), 0o600))
			cases = append(cases, messageCase{text: text, refuse: gitIn(t, dir, "interpret-trailers", "--parse", file) != ""})
		}
	}

	return cases
}
