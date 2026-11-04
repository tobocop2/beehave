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
	"time"

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
	assert.NotContains(t, text, "agent:fix")
	assert.NotContains(t, text, "FAILED")
}

func TestDryRun_AnAgentWithAStepCapRunsAtCostZero(t *testing.T) {
	t.Parallel()

	file := dryFile(t, bodyFlow(`a = agent(model = "m", prompt = "a.md", max_budget_usd = 2.5, name = "a")
command(run = ["show", str(a.cost_usd)], name = "show")
`))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "a.md"), []byte("hello\n"), 0o600))

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), file, "12", map[string]string{}, &out), out.String())
	assert.NotContains(t, out.String(), "FAILED")
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

func TestDryRun_TheWritesToAPullRequestAndAnItemAreShownAndChangeNothing(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	body := "def main(item):\n    pr_comment(text = \"x\", key = \"k\", name = \"c\")\n    pr_assign(assignee = \"alice\", linked = True, name = \"a\")\n    pr_open(title = \"t\", base = \"b\", name = \"o\")\n    item_assign(id = \"1\", assignee = \"alice\", name = \"i\")\n    board_sync(board = 3, status = \"Done\", name = \"b\")\n"
	require.NoError(t, DryRun(t.Context(), dryFile(t, body), "12", map[string]string{}, &out), out.String())

	assert.Contains(t, out.String(), "board_sync:b#1")

	assert.Contains(t, out.String(), "pr_comment:c#1")
	assert.Contains(t, out.String(), "pr_assign:a#1")
	assert.Contains(t, out.String(), "pr_open:o#1")
	assert.Contains(t, out.String(), "item_assign:i#1")
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

			err := DryRun(t.Context(), dryFile(t, "def main(item):\n    "+strings.ReplaceAll(strings.TrimSpace(tc.body), "\n", "\n    ")+"\n"), "12", map[string]string{}, &out)
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

const dryGateAndStop = "def main(item):\n    g = gate(name = \"check\")\n    if not g.passed:\n        fail(\"the gate is red\")\n"

func TestDryRun_ADefaultRunPassesAWorkflowThatStopsAtARedGate(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryFile(t, dryGateAndStop), "12", map[string]string{}, &out), out.String())
}

func TestDryRun_ARedGateRunEndsAWorkflowThatStopsAtARedGateWithAnError(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), dryFile(t, dryGateAndStop), "12", map[string]string{}, &out, WithRedGate())

	require.Error(t, err)
	assert.Contains(t, out.String(), "the gate is red")
}

func TestDryRun_TheFirstGateIsRedAndTheRunWalksAFixRoundBeforeTheGatePasses(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), filepath.Join("..", "..", "examples", "remediation", "workflow.star"), "12", dryExampleEnv, &out, WithRedGate()), out.String())

	text := out.String()
	fix := strings.Index(text, "agent:fix")
	require.GreaterOrEqual(t, fix, 0, "no fix round ran:\n"+text)

	first := strings.Index(text, "gate:check#1")
	last := strings.LastIndex(text, "gate:check#")
	assert.Less(t, first, fix, "the fix round follows the red gate")
	assert.Greater(t, last, fix, "a gate follows the fix round")
}

func TestDryGate_EveryRunPassesWithAPassedStepUnlessTheRedGateIsChosen(t *testing.T) {
	t.Parallel()

	gate := newDryGate("h", false)

	for range 2 {
		green, err := gate.Run(t.Context(), "p", "i", GateJob{})
		require.NoError(t, err)
		assert.True(t, green.Passed)
		assert.Equal(t, []GateStep{{Name: "command", Status: "pass"}}, green.Steps)
	}
}

func TestDryGate_WithTheRedGateTheFirstRunIsRedWithAFailedStepAndEveryLaterRunPasses(t *testing.T) {
	t.Parallel()

	gate := newDryGate("h", true)

	red, err := gate.Run(t.Context(), "p", "i", GateJob{})
	require.NoError(t, err)
	assert.False(t, red.Passed)
	assert.Equal(t, []GateStep{{Name: "command", Status: "fail"}}, red.Steps)

	for range 2 {
		green, err := gate.Run(t.Context(), "p", "i", GateJob{})
		require.NoError(t, err)
		assert.True(t, green.Passed)
		assert.Equal(t, []GateStep{{Name: "command", Status: "pass"}}, green.Steps)
	}
}

// remediationStepNames dry-runs the remediation example and returns the names of its steps in order.
func remediationStepNames(t *testing.T, options ...DryOption) []string {
	t.Helper()

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), filepath.Join("..", "..", "examples", "remediation", "workflow.star"), "12", dryExampleEnv, &out, options...), out.String())

	var names []string

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		fields := strings.Fields(line)
		require.GreaterOrEqual(t, len(fields), 3, line)
		assert.Equal(t, strconv.Itoa(len(names)+1), fields[1], line)

		names = append(names, strings.TrimSuffix(fields[2], ":"))
	}

	return names
}

func TestDryRun_TheStepListOfTheRemediationExampleIsComplete(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		"claim", "item", "env:QUEUE_SWEEP#1", "env:TAKE_PR#2", "env:ROUND_STEP#3", "env:HOST_UP#4", "env:LAND#5", "env:EXPECT_HEAD#6", "env:BASE#7", "env:OWNER_LOGIN#8", "preflight:intake#1", "env:LANE_KIND#9", "env:PATCH_ROUNDS#10", "facts#1", "login#1",
		"agent:plan#1/render", "agent:plan#1/clear1", "phase", "DBOS.getResult", "agent:plan#1/read1", "env:SOURCE_CHECKOUT#11",
		"agent:implement#2/render", "agent:implement#2/clear1", "phase", "DBOS.getResult", "agent:implement#2/read1", "env:BASE#12", "env:PARENT#13", "pr_open:draft#1", "env:BASE#14", "env:OWNER_LOGIN#15", "env:SOURCE_CHECKOUT#16",
		"gate:tip", "exclusive", "gate:stop", "DBOS.getResult", "scope:check#1", "gate:check#1", "preflight:before-review-1#2",
		"agent:review#3/render", "agent:review#3/clear1", "phase", "DBOS.getResult", "agent:review#3/read1",
		"env:MERGE#17", "env:REPO#18", "env:BASE#19", "env:OWNER_LOGIN#20", "env:OWNER_EMAIL#21", "ready", "DBOS.setEvent", "watch", "watch", "merge:ship#1",
		"env:HOST_TEARDOWN#22", "worktree", "release",
	}, remediationStepNames(t))
}

func TestDryRun_TheStepListOfTheRemediationExampleWithARedGateHoldsTheFixRound(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		"claim", "item", "env:QUEUE_SWEEP#1", "env:TAKE_PR#2", "env:ROUND_STEP#3", "env:HOST_UP#4", "env:LAND#5", "env:EXPECT_HEAD#6", "env:BASE#7", "env:OWNER_LOGIN#8", "preflight:intake#1", "env:LANE_KIND#9", "env:PATCH_ROUNDS#10", "facts#1", "login#1",
		"agent:plan#1/render", "agent:plan#1/clear1", "phase", "DBOS.getResult", "agent:plan#1/read1", "env:SOURCE_CHECKOUT#11",
		"agent:implement#2/render", "agent:implement#2/clear1", "phase", "DBOS.getResult", "agent:implement#2/read1", "env:BASE#12", "env:PARENT#13", "pr_open:draft#1", "env:BASE#14", "env:OWNER_LOGIN#15", "env:SOURCE_CHECKOUT#16",
		"gate:tip", "exclusive", "gate:stop", "DBOS.getResult", "scope:check#1", "gate:check#1", "preflight:before-fix-1#2",
		"agent:fix#3/render", "agent:fix#3/clear1", "phase", "DBOS.getResult", "agent:fix#3/read1",
		"gate:tip", "exclusive", "gate:stop", "DBOS.getResult", "gate:check#2", "preflight:before-review-2#3",
		"agent:review#4/render", "agent:review#4/clear1", "phase", "DBOS.getResult", "agent:review#4/read1",
		"env:MERGE#17", "env:REPO#18", "env:BASE#19", "env:OWNER_LOGIN#20", "env:OWNER_EMAIL#21", "ready", "DBOS.setEvent", "watch", "watch", "merge:ship#1",
		"env:HOST_TEARDOWN#22", "worktree", "release",
	}, remediationStepNames(t, WithRedGate()))
}

func TestPrintDrySteps_FailsWhenTheStoreCannotOpenAndWhenTheClientIsClosed(t *testing.T) {
	t.Parallel()

	_, err := printDrySteps(t.Context(), Options{}, &bytes.Buffer{})
	require.Error(t, err)

	h := newHarness(t)

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

func TestDryDeps_NowIsTheFixedClockOfADryRun(t *testing.T) {
	t.Parallel()

	deps := dryDeps("/t", "h", "12", nil, false)

	assert.Equal(t, dryNow(), deps.Clock.reading())
	assert.Equal(t, "2030-01-02T03:04:05Z", dryNow().Format(time.RFC3339))
}

func TestDryRun_NowPrintsTheSameTextInTwoRuns(t *testing.T) {
	t.Parallel()

	file := dryFile(t, "def main(item):\n    fail(\"AT \" + now())\n")

	var first, second bytes.Buffer

	require.Error(t, DryRun(t.Context(), file, "12", map[string]string{}, &first))
	require.Error(t, DryRun(t.Context(), file, "12", map[string]string{}, &second))
	assert.Contains(t, first.String(), "AT 2030-01-02T03:04:05Z")
	assert.Equal(t, first.String(), second.String())
}

func TestDryDeps_EveryCollaboratorIsADryOne(t *testing.T) {
	t.Parallel()

	deps := dryDeps("/t", "h", "12", nil, false)

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
	assert.JSONEq(t, `{"verdict":"approve","summary":"0","findings":[]}`, string(raw))
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
	}

	for name, env := range envs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			require.NoError(t, DryRun(t.Context(), filepath.Join("..", "..", "examples", name, "workflow.star"), "12", env, &out), out.String())
		})
	}
}

func TestDryRun_TheSwarmExampleFilesItsBacklogAndChecksTheBudgetThenEndsEarlyAtTheReadOfTheOpenWork(t *testing.T) {
	t.Parallel()

	env := map[string]string{"SWARM_DIR": "/swarm", "SWARM": "s1", "REPO": "o/r", "BASE": "integration", "OWNER_LOGIN": "owner", "OWNER_EMAIL": "owner@example.com", "CAP_USD": "100"}

	var out bytes.Buffer

	err := DryRun(t.Context(), filepath.Join("..", "..", "examples", "swarm", "workflow.star"), "12", env, &out)
	require.ErrorIs(t, err, ErrDryRunEndedEarly, out.String())

	assert.Contains(t, out.String(), "item_create:create-1")
	assert.Contains(t, out.String(), "command:budget-0")
	assert.Contains(t, out.String(), "not run in a dry run: read the open work of the project")
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
