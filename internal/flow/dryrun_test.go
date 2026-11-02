package flow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	trackerstore "github.com/tobocop2/obee/internal/store"
)

var dryExampleEnv = map[string]string{"REPO": "o/r", "BASE": "integration", "OWNER_LOGIN": "owner", "OWNER_EMAIL": "owner@example.com", "SOURCE_CHECKOUT": "/own/checkout", "MERGE": "false", "EXPECT_HEAD": ""}

func dryFile(t *testing.T, text string) string {
	t.Helper()

	return writeFlow(t, text)
}

func TestDryRun_PrintsEachStepOfTheExampleInOrderAndEndsClean(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), filepath.Join("..", "..", "examples", "remediation", "workflow.star"), "12", dryExampleEnv, &out)
	require.NoError(t, err, out.String())

	text := out.String()
	assert.Contains(t, text, "step 1 ")
	assert.Contains(t, text, "dry run: no agent ran")
	assert.Positive(t, strings.Index(text, " agent:plan#1"), text)
	assert.Less(t, strings.Index(text, " agent:plan#1"), strings.Index(text, " agent:implement#2"), text)
	assert.Less(t, strings.Index(text, " agent:implement#2"), strings.Index(text, " agent:review#3"), text)
	assert.NotContains(t, text, "FAILED")
}

func TestDryRun_AFailedRunNamesTheErrorWithItsLineAndTheLastStepOnItsOwnLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), dryFile(t, "def main(item):\n    x = 1 // 0\n"), "12", map[string]string{}, &out)
	require.Error(t, err)

	text := out.String()
	assert.Regexp(t, `(?m)^error: .*:2:\d+: .*$`, text)
	assert.Regexp(t, `(?m)^the run stopped after step \d+ \S+$`, text)
	assert.NotContains(t, text, "error after")
}

func TestDryRun_AStepThatTheDryRunDoesNotFakeIsNotRunAndIsNotAnError(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ body, want string }{
		"host":  {body: "host_teardown(name = \"stop\")", want: "not run in a dry run: stop the build host"},
		"queue": {body: "queue(name = \"q\")", want: "not run in a dry run: read the open work of the project"},
		"merge": {body: shipCall("merge = True, "), want: "not run in a dry run: merge the pull request"},
		"batch": {body: "batch_merge(prs = [struct(item = \"1\", head = \"h\")], base = \"main\", repo = \"o/r\", name = \"b\")", want: "not run in a dry run: merge a batch of pull requests"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			err := DryRun(t.Context(), dryFile(t, "def main(item):\n    "+strings.TrimSpace(tc.body)+"\n"), "12", map[string]string{}, &out)
			require.ErrorIs(t, err, ErrDryRunEndedEarly, out.String())

			assert.Contains(t, out.String(), tc.want)
			assert.Contains(t, out.String(), "\nthe dry run ended early at a step it does not fake: ")
			assert.NotContains(t, out.String(), "this build cannot")
		})
	}
}

func TestDryRun_RemovesTheTemporaryFolderAfterAFailedRun(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)

	err := DryRun(t.Context(), dryFile(t, "def main(item):\n    fail(\"boom\")\n"), "12", map[string]string{}, &bytes.Buffer{})
	require.Error(t, err)

	left, readErr := os.ReadDir(temp)
	require.NoError(t, readErr)
	assert.Empty(t, left)
}

func TestDryRun_RemovesTheTemporaryFolderAfterASuccessfulRun(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryFile(t, "def main(item):\n    pass\n"), "12", map[string]string{}, &out), out.String())

	left, readErr := os.ReadDir(temp)
	require.NoError(t, readErr)
	assert.Empty(t, left)
}

func TestDryRun_TheCleanupStepsReadARemoteInsteadOfPrintingAGitError(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), dryFile(t, "def main(item):\n    fail(\"boom\")\n"), "12", map[string]string{}, &out)
	require.Error(t, err)

	assert.NotContains(t, out.String(), "could not read the branches of the remote")
	assert.NotContains(t, out.String(), "does not appear to be a git repository")
}

func TestDryRun_TheStepListOfTheRemediationExampleIsComplete(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), filepath.Join("..", "..", "examples", "remediation", "workflow.star"), "12", dryExampleEnv, &out), out.String())

	var names []string

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		fields := strings.Fields(line)
		require.GreaterOrEqual(t, len(fields), 3, line)
		assert.Equal(t, strconv.Itoa(len(names)+1), fields[1], line)

		names = append(names, strings.TrimSuffix(fields[2], ":"))
	}

	assert.Equal(t, []string{
		"claim", "item", "env:HOST_UP#1", "env:LAND#2", "env:EXPECT_HEAD#3", "env:BASE#4", "env:OWNER_LOGIN#5", "preflight:intake#1", "env:LANE_KIND#6", "login#1",
		"agent:plan#1/render", "agent:plan#1/clear1", "phase", "DBOS.getResult", "agent:plan#1/read1", "env:SOURCE_CHECKOUT#7",
		"agent:implement#2/render", "agent:implement#2/clear1", "phase", "DBOS.getResult", "agent:implement#2/read1", "env:BASE#8", "env:OWNER_LOGIN#9", "env:SOURCE_CHECKOUT#10",
		"gate:tip", "exclusive", "gate:stop", "DBOS.getResult", "scope:check#1", "gate:check#1", "preflight:before-review-1#2",
		"agent:review#3/render", "agent:review#3/clear1", "phase", "DBOS.getResult", "agent:review#3/read1",
		"env:MERGE#11", "env:REPO#12", "env:BASE#13", "env:OWNER_LOGIN#14", "env:OWNER_EMAIL#15", "ready", "DBOS.setEvent", "watch", "watch", "merge:ship#1",
		"env:HOST_TEARDOWN#16", "worktree", "release",
	}, names)
}

func TestPrintDrySteps_FailsWhenTheStoreCannotOpenAndWhenTheClientIsClosed(t *testing.T) {
	t.Parallel()

	_, err := printDrySteps(t.Context(), Options{}, &bytes.Buffer{})
	require.Error(t, err)

	h := newHarness(t)
	h.migrated(t)

	client, err := OpenClient(t.Context(), h.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	lines, last, err := dryLines(client)
	require.Error(t, err)
	assert.Empty(t, lines)
	assert.Equal(t, "the start", last)
}

func TestDryRun_ARefuseInTheFilePrintsTheErrorAndReturnsIt(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), dryFile(t, "def main(item):\n    fail(\"REFUSE: not today\")\n"), "12", map[string]string{}, &out)
	require.ErrorContains(t, err, "REFUSE: not today")
	assert.Contains(t, out.String(), "error: ")
	assert.Contains(t, out.String(), "REFUSE: not today")
}

func TestDryRun_RefusesAMissingFileAndASyntaxError(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.Error(t, DryRun(t.Context(), filepath.Join(t.TempDir(), "none.star"), "12", map[string]string{}, &out))
	require.Error(t, DryRun(t.Context(), dryFile(t, "def main(:\n"), "12", map[string]string{}, &out))
}

func TestDryRun_RefusesAStoppedContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.Error(t, DryRun(ctx, dryFile(t, defaultFlow), "12", map[string]string{}, &bytes.Buffer{}))
}

func TestDryRun_RefusesAnEnvironmentThatSetsDBOSVariables(t *testing.T) {
	t.Parallel()

	require.Error(t, DryRun(t.Context(), dryFile(t, defaultFlow), "12", map[string]string{"DBOS__APPVERSION": "x"}, &bytes.Buffer{}))
}

func TestDryRun_ARunThatEndsAtAStepItDoesNotFakeNamesTheStepOnTheLastLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), dryFile(t, "def main(item):\n    host_teardown(name = \"stop\")\n"), "12", map[string]string{}, &out)
	require.ErrorIs(t, err, ErrDryRunEndedEarly)

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	assert.Equal(t, "the dry run ended early at a step it does not fake: stop the build host; the steps after it were not checked", lines[len(lines)-1])
}

func TestDryRun_AFullRunDoesNotReturnTheEndedEarlyError(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryFile(t, "def main(item):\n    pass\n"), "12", map[string]string{}, &out), out.String())
	assert.NotContains(t, out.String(), "ended early")
}

func TestDryFailure_AnAbsentErrorJoinedWithARealErrorIsAFailureAndPrintsBoth(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := dryFailure(&out, "step 3 x", fmt.Errorf("gather: %w", errors.Join(errNoQueue, errors.New("the child failed for real"))))

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrDryRunEndedEarly)
	assert.Contains(t, out.String(), "the child failed for real")
	assert.Contains(t, out.String(), errNoQueue.Error())
	assert.Contains(t, out.String(), "the run stopped after step 3 x")
	assert.NotContains(t, out.String(), "not run in a dry run")
}

func TestDryFailure_SeveralAbsentErrorsAndNothingElseIsAPartialRun(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := dryFailure(&out, "step 3 x", errors.Join(errNoQueue, errNoHosts))

	require.ErrorIs(t, err, ErrDryRunEndedEarly)
	assert.Contains(t, out.String(), "the dry run ended early at a step it does not fake: ")
}

func TestDryRun_TheUsersGitConfigDoesNotRunInTheDryRepository(t *testing.T) {
	home := t.TempDir()
	hooks := filepath.Join(home, "hooks")
	require.NoError(t, os.MkdirAll(hooks, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o700)) //nolint:gosec // a test hook must run
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\n\thooksPath = "+hooks+"\n[commit]\n\tgpgsign = true\n[init]\n\ttemplateDir = "+filepath.Join(home, "missing")+"\n"), 0o600))
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryFile(t, "def main(item):\n    pass\n"), "12", map[string]string{}, &out), out.String())
}

func TestDryRun_TheTemplateDirOfTheEnvironmentCopiesNothingIntoTheDryRepository(t *testing.T) {
	template := t.TempDir()
	hooks := filepath.Join(template, "hooks")
	require.NoError(t, os.MkdirAll(hooks, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o700)) //nolint:gosec // a test hook must run
	t.Setenv("GIT_TEMPLATE_DIR", template)

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryFile(t, "def main(item):\n    pass\n"), "12", map[string]string{}, &out), out.String())
}

func TestDryDeps_EveryCollaboratorIsADryOne(t *testing.T) {
	t.Parallel()

	deps := dryDeps("/t", "h", "12", nil)

	for name, held := range map[string]any{
		"Agents": deps.Agents, "Gate": deps.Gate, "Logins": deps.Logins, "Lifecycle": deps.Lifecycle, "Forge": deps.Forge, "Watch": deps.Watch,
		"Trees": deps.Trees, "Beats": deps.Beats, "Commands": deps.Commands, "Items": deps.Items, "Writer": deps.Writer, "Preflight": deps.Preflight,
	} {
		assert.Contains(t, fmt.Sprintf("%T", held), "flow.dry", name)
	}

	assert.Empty(t, deps.Kept)
	assert.Nil(t, deps.Merge)
	assert.Nil(t, deps.Batch)
	assert.Nil(t, deps.Queue)
	assert.Nil(t, deps.Poll)
	assert.Nil(t, deps.Hosts)
}

func TestShorten_CutsAtACharacterNotAByte(t *testing.T) {
	t.Parallel()

	cut := shorten(strings.Repeat("é", dryOutLimit+5))

	assert.Equal(t, strings.Repeat("é", dryOutLimit)+"...", cut)
}

func TestDryLine_ShowsTheErrorOrTheResultOnOneLine(t *testing.T) {
	t.Parallel()

	assert.Contains(t, dryLine(1, dbos.StepInfo{StepName: "a", Error: assert.AnError}), "FAILED")
	assert.Equal(t, `step 2 b: map[k:1]`, dryLine(2, dbos.StepInfo{StepName: "b", Output: map[string]int{"k": 1}}))
	assert.Equal(t, "step 3 c: a b", dryLine(3, dbos.StepInfo{StepName: "c", Output: "a\n b"}))
	assert.True(t, strings.HasSuffix(dryLine(4, dbos.StepInfo{StepName: "d", Output: strings.Repeat("x", 500)}), "..."))
}

func TestDryRepo_FailsWhenTheFolderCannotBeMade(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	_, err := dryRepo(t.Context(), filepath.Join(file, "sub"), t.TempDir()+"/o")
	require.Error(t, err)

	_, err = dryRepo(t.Context(), t.TempDir()+"/ok", filepath.Join(file, "sub"))
	require.Error(t, err)

	_, err = dryRepo(t.Context(), t.TempDir()+"/ok", t.TempDir()+"/o")
	require.NoError(t, err)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = dryRepo(canceled, t.TempDir(), t.TempDir())
	require.Error(t, err)
}

func TestDryRun_FailsWhenTheTemporaryFolderCannotBeMade(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "none"))

	require.Error(t, DryRun(t.Context(), dryFile(t, defaultFlow), "12", map[string]string{}, &bytes.Buffer{}))
}

func TestDryAgents_FailsWhenTheReviewFileCannotBeWritten(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	_, err := dryAgents{}.Run(t.Context(), AgentRequest{OutputFile: filepath.Join(file, "out.json"), Outputs: []string{"verdict"}})
	require.Error(t, err)
}

func TestDryAgents_WriteTheDeclaredOutputsOfTheRequestWhateverThePromptSays(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "outputs", "out.json")

	_, err := dryAgents{}.Run(t.Context(), AgentRequest{Prompt: "Judge the change.", OutputFile: file, Outputs: []string{"verdict", "summary"}})
	require.NoError(t, err)

	raw, err := os.ReadFile(file) //nolint:gosec // the test names its own output file
	require.NoError(t, err)
	assert.JSONEq(t, `{"verdict":"approve","summary":"dry","findings":[]}`, string(raw))
}

func TestDryAgents_WriteNoFileForACallWithNoDeclaredOutputs(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "out.json")

	_, err := dryAgents{}.Run(t.Context(), AgentRequest{Prompt: "Write it to " + file + " as JSON", OutputFile: file})
	require.NoError(t, err)
	assert.NoFileExists(t, file)
}

func TestDryRun_AReviewPromptWithNoWriteInstructionStillGetsItsVerdict(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p.md"), []byte("Judge the change. Nothing else."), 0o600))
	flow := filepath.Join(dir, "workflow.star")
	require.NoError(t, os.WriteFile(flow, []byte("def main(item):\n    r = agent(prompt = \"p.md\", model = \"sonnet\", outputs = [\"verdict\"])\n    if r.verdict !=\"approve\":\n        fail(\"not approved\")\n"), 0o600))

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), flow, "12", map[string]string{}, &out), out.String())
}

func TestDryRun_EveryExampleThatNeedsNoServiceRunsToTheEnd(t *testing.T) {
	t.Parallel()

	envs := map[string]map[string]string{
		"local":       {},
		"remediation": dryExampleEnv,
		"research":    {"RESEARCH_TOOLS": "/tools", "RESEARCH_OUT": "/out"},
		"swarm":       dryExampleEnv,
	}

	for name, env := range envs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			require.NoError(t, DryRun(t.Context(), filepath.Join("..", "..", "examples", name, "workflow.star"), "12", env, &out), out.String())
		})
	}
}

func TestDryRun_TheQueueExampleEndsEarlyAtTheReadOfTheOpenWork(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), filepath.Join("..", "..", "examples", "queue", "workflow.star"), "12", map[string]string{}, &out)
	require.ErrorIs(t, err, ErrDryRunEndedEarly, out.String())
}

func TestDryFakes_AnswerWithoutTouchingAnything(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	lost, err := dryBeats{}.Beat(ctx, "p", "o")
	require.NoError(t, err)
	assert.Empty(t, lost)

	renewed, err := dryBeats{}.Renewed(ctx, "p", "o", "1")
	require.NoError(t, err)
	assert.True(t, renewed.IsZero())

	require.NoError(t, dryLifecycle{}.Release(ctx, "p", "1", "o"))
	require.NoError(t, dryForge{}.Ready(ctx, "p", "1"))
	require.NoError(t, dryWatch{}.SetDraft(ctx, "p", 1, true))
	require.NoError(t, dryWriter{}.SetStatus(ctx, "p", "1", ""))
	require.NoError(t, dryWriter{}.Comment(ctx, "p", "1", "k", "t"))

	id, created, err := dryWriter{}.Create(ctx, "p", "k", trackerstore.NewItem{})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "dry-1", id)

	dir, err := dryTrees{dir: "/t"}.Ensure(ctx, "p", "1")
	require.NoError(t, err)
	assert.Equal(t, "/t", dir)

	out, err := dryCommands{}.Run(ctx, CommandRequest{})
	require.NoError(t, err)
	assert.Equal(t, CommandOutput{}, out)
}
