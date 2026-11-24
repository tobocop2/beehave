package flow

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
	trackerstore "github.com/tobocop2/beehave/internal/store"
)

// dryRoundFile is the file of the example as it is. A dry run of it runs the round, which ends at the read of the queue.
var dryRoundFile = filepath.Join("..", "..", "examples", "remediation", "workflow.star")

// dryPipeline starts a dry run at the entry pipeline of the example, so that it walks the gate, the review and the fix round of one item.
var dryPipeline = WithEntry("pipeline")

// dryRound gives the dry run the vars of the example project: the round settings, which every entry of the example reads, and the command of the mutation step.
var dryRound = WithVars(with(roundVars(map[string]any{
	"base": "integration", "owner": "owner", "owner_email": "owner@example.com", "source_checkout": "/own/checkout", "merge": false,
	"lane_cap": json.Number("3"), "fix_rounds": json.Number("5"), "label": "beehave-round",
}), map[string]any{"mutation_command": []any{"sh", "scripts/mutation-pr.sh"}, "review_skill": exampleSkill}))

func dryFile(t *testing.T, text string) string {
	t.Helper()

	return writeFlow(t, text)
}

func TestDryRun_ThePipelineOfTheExamplePrintsEachStepInOrderAndEndsClean(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), dryRoundFile, "12", nil, &out, dryPipeline, dryRound)
	require.NoError(t, err, out.String())

	text := out.String()
	assert.Contains(t, text, "step 1 ")
	// The listing clips each line at dryOutLimit, so the test reads the head of each render record: the kind, the login and the model come first.
	assert.Equal(t, 5, strings.Count(text, `"login":"dry-run","model":`), "each of the five agent calls records the fake login and its model before the agent runs")
	assert.Contains(t, text, `"model":"opus"`)
	assert.Contains(t, text, `"model":"sonnet"`)
	assert.Positive(t, strings.Index(text, " agent:plan#1"), text)
	assert.Less(t, strings.Index(text, " agent:plan#1"), strings.Index(text, " agent:implement#2"), text)
	assert.Less(t, strings.Index(text, " agent:implement#2"), strings.Index(text, " agent:implement-sweep#3"), text)
	assert.Less(t, strings.Index(text, " agent:implement-sweep#3"), strings.Index(text, " agent:scoped-review-implement#4"), text)
	assert.Less(t, strings.Index(text, " agent:scoped-review-implement#4"), strings.Index(text, " agent:candidate-review#5"), text)
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

func TestDryRun_ACommandWithASlotClassRunsItsFakeProgram(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	file := dryFile(t, bodyFlow(`command(run = ["mutate"], host = "box", slots = "mutation", name = "mutate")`))

	require.NoError(t, DryRun(t.Context(), file, "12", map[string]string{}, &out), out.String())
	assert.Contains(t, out.String(), "command:mutate")
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

	body := "def main(item):\n    pr_comment(text = \"x\", key = \"k\", name = \"c\")\n    pr_open(title = \"t\", base = \"b\", name = \"o\")\n    item_create(title = \"t\", key = \"k\", assignee = \"alice\", name = \"i\")\n"
	require.NoError(t, DryRun(t.Context(), dryFile(t, body), "12", map[string]string{}, &out), out.String())

	assert.Contains(t, out.String(), "pr_comment:c#1")
	assert.Contains(t, out.String(), "pr_open:o#1")
	assert.Contains(t, out.String(), "item_create:i#1")
}

func TestDryRun_APushIsShownAndReturnsTheHeadWithNoPush(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	body := "def main(item):\n    pushed = push(branch = \"b\", head = \"abc\", name = \"p\")\n    command(run = [pushed.head], name = \"seen\")\n"
	require.NoError(t, DryRun(t.Context(), dryFile(t, body), "12", map[string]string{}, &out), out.String())

	assert.Contains(t, out.String(), "push:p#1")
	assert.Contains(t, out.String(), "command:seen#1")
}

func TestDryRun_AStepThatTheDryRunDoesNotFakeIsNotRunAndIsNotAnError(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ body, want string }{
		"host":  {body: "host_teardown(name = \"stop\")", want: "not run in a dry run: stop the build host"},
		"queue": {body: "queue(name = \"q\")", want: "not run in a dry run: read the open work of the project"},
		"state": {body: "pr_state(pr = 41, name = \"s\")", want: "not run in a dry run: read the state of a pull request"},
		"batch": {body: "merge(prs = [struct(item = \"1\", head = \"h\", closes = [])], base = \"main\", repo = \"o/r\", name = \"b\")", want: "not run in a dry run: merge a batch of pull requests"},
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

	const text = "def main(item):\n    g = gate(name = \"check\")\n    if not g.passed:\n        agent(name = \"fix\", prompt = \"p.md\", model = \"sonnet\")\n        gate(name = \"check\")\n"

	require.NoError(t, DryRun(t.Context(), dryFileWithPrompt(t, text), "12", nil, &out, WithRedGate()), out.String())

	got := out.String()
	fix := strings.Index(got, "agent:fix")
	require.GreaterOrEqual(t, fix, 0, "no fix round ran:\n"+got)

	first := strings.Index(got, "gate:check#1")
	last := strings.LastIndex(got, "gate:check#")
	assert.Less(t, first, fix, "the fix round follows the red gate")
	assert.Greater(t, last, fix, "a gate follows the fix round")
}

// dryFileWithPrompt writes the workflow text beside a prompt file p.md and returns the workflow file.
func dryFileWithPrompt(t *testing.T, text string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p.md"), []byte("Judge the change. Nothing else."), 0o600))

	flow := filepath.Join(dir, "workflow.star")
	require.NoError(t, os.WriteFile(flow, []byte(text), 0o600))

	return flow
}

func TestDryGate_EveryRunPassesWithAPassedStepUnlessTheRedGateIsChosen(t *testing.T) {
	t.Parallel()

	gate := newDryGate(t.TempDir(), "h", false)

	for range 2 {
		green, err := gate.Run(t.Context(), "p", "i", "", GateJob{})
		require.NoError(t, err)
		assert.True(t, green.Passed)
		assert.Equal(t, []GateStep{{Name: "command", Status: "pass"}}, green.Steps)
	}
}

func TestDryGate_WithTheRedGateTheFirstRunIsRedWithAFailedStepAndEveryLaterRunPasses(t *testing.T) {
	t.Parallel()

	gate := newDryGate(t.TempDir(), "h", true)

	red, err := gate.Run(t.Context(), "p", "i", "", GateJob{})
	require.NoError(t, err)
	assert.False(t, red.Passed)
	assert.Equal(t, []GateStep{{Name: "command", Status: "fail"}}, red.Steps)

	for range 2 {
		green, err := gate.Run(t.Context(), "p", "i", "", GateJob{})
		require.NoError(t, err)
		assert.True(t, green.Passed)
		assert.Equal(t, []GateStep{{Name: "command", Status: "pass"}}, green.Steps)
	}
}

// remediationStepNames dry-runs the pipeline of the remediation example with the round settings of the example project, and returns the names of its steps in order.
func remediationStepNames(t *testing.T, options ...DryOption) []string {
	t.Helper()

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryRoundFile, "12", nil, &out, append([]DryOption{dryPipeline, dryRound}, options...)...), out.String())

	var names []string

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		fields := strings.Fields(line)
		require.GreaterOrEqual(t, len(fields), 3, line)
		assert.Equal(t, strconv.Itoa(len(names)+1), fields[1], line)

		names = append(names, strings.TrimSuffix(fields[2], ":"))
	}

	return names
}

// The pipeline of the example ends at the review: the round lands the approved head, so the step list of one item holds no merge.
func TestDryRun_TheStepListOfThePipelineOfTheExampleIsComplete(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		"claim", "item", "facts#1", "scope:intake#1", "preflight:intake#1", "pr_history:history#1", "earlier_calls:spent-fix-scoped-1#1", "earlier_calls:spent-scoped-review-implement#2", "item_read:parent#1", "login#1",
		"agent:plan#1/render", "agent:plan#1/clear1", "phase", "DBOS.getResult", "agent:plan#1/read1",
		"agent:implement#2/start", "agent:implement#2/render", "agent:implement#2/clear1", "phase", "DBOS.getResult", "agent:implement#2/read1", "agent:implement#2/commit",
		"agent:implement-sweep#3/start", "agent:implement-sweep#3/render", "agent:implement-sweep#3/clear1", "phase", "DBOS.getResult", "agent:implement-sweep#3/read1", "agent:implement-sweep#3/commit", "push:push-implement#1", "pr_open:draft#1",
		"preflight:before-scoped-review-1#2",
		"agent:scoped-review-implement#4/render", "agent:scoped-review-implement#4/clear1", "phase", "DBOS.getResult", "agent:scoped-review-implement#4/read1",
		"gate:tip", "exclusive", "gate:stop", "DBOS.getResult", "gate:candidate-gate#1", "facts#2", "command:candidate-before-review-ancestor#1", "command:candidate-before-review-keywords#2",
		"command:candidate-lane-report#3", "host_job", "DBOS.getResult", "command:mutation#4", "preflight:before-review-1#3",
		"agent:candidate-review#5/render", "agent:candidate-review#5/clear1", "phase", "DBOS.getResult", "agent:candidate-review#5/read1", "pr_comment:approved#1",
		"worktree", "release",
	}, remediationStepNames(t))
}

func TestDryRun_TheStepListOfThePipelineOfTheExampleWithARedGateHoldsTheFixRound(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		"claim", "item", "facts#1", "scope:intake#1", "preflight:intake#1", "pr_history:history#1", "earlier_calls:spent-fix-scoped-1#1", "earlier_calls:spent-scoped-review-implement#2", "item_read:parent#1", "login#1",
		"agent:plan#1/render", "agent:plan#1/clear1", "phase", "DBOS.getResult", "agent:plan#1/read1",
		"agent:implement#2/start", "agent:implement#2/render", "agent:implement#2/clear1", "phase", "DBOS.getResult", "agent:implement#2/read1", "agent:implement#2/commit",
		"agent:implement-sweep#3/start", "agent:implement-sweep#3/render", "agent:implement-sweep#3/clear1", "phase", "DBOS.getResult", "agent:implement-sweep#3/read1", "agent:implement-sweep#3/commit", "push:push-implement#1", "pr_open:draft#1",
		"preflight:before-scoped-review-1#2",
		"agent:scoped-review-implement#4/render", "agent:scoped-review-implement#4/clear1", "phase", "DBOS.getResult", "agent:scoped-review-implement#4/read1",
		"gate:tip", "exclusive", "gate:stop", "DBOS.getResult", "gate:candidate-gate#1", "preflight:before-fix-2#3",
		"agent:fix-scoped-1#5/start", "agent:fix-scoped-1#5/render", "agent:fix-scoped-1#5/clear1", "phase", "DBOS.getResult", "agent:fix-scoped-1#5/read1", "agent:fix-scoped-1#5/commit", "push:push-fix-1#2",
		"preflight:before-scoped-review-2#4",
		"agent:scoped-review-1#6/render", "agent:scoped-review-1#6/clear1", "phase", "DBOS.getResult", "agent:scoped-review-1#6/read1",
		"gate:tip", "exclusive", "gate:stop", "DBOS.getResult", "gate:candidate-gate#2", "facts#2", "command:candidate-before-review-ancestor#1", "command:candidate-before-review-keywords#2",
		"command:candidate-lane-report#3", "host_job", "DBOS.getResult", "command:mutation#4", "preflight:before-review-2#5",
		"agent:candidate-review#7/render", "agent:candidate-review#7/clear1", "phase", "DBOS.getResult", "agent:candidate-review#7/read1", "pr_comment:approved#1",
		"worktree", "release",
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
	require.NoError(t, exectest.Write(filepath.Join(hooks, "pre-commit"), "#!/bin/sh\nexit 1\n"))
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
	require.NoError(t, exectest.Write(filepath.Join(hooks, "pre-commit"), "#!/bin/sh\nexit 1\n"))
	t.Setenv("GIT_TEMPLATE_DIR", template)

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryFile(t, "def main(item):\n    pass\n"), "12", map[string]string{}, &out), out.String())
}

func TestDryDeps_NowIsTheFixedClockOfADryRun(t *testing.T) {
	t.Parallel()

	deps := dryDeps("/t", "h", "12", dryChoice{})

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

	deps := dryDeps("/t", "h", "12", dryChoice{})

	for name, held := range map[string]any{
		"Agents": deps.Agents, "Gate": deps.Gate, "Logins": deps.Logins, "Lifecycle": deps.Lifecycle, "Forge": deps.Forge,
		"Trees": deps.Trees, "Beats": deps.Beats, "Commands": deps.Commands, "Items": deps.Items, "Writer": deps.Writer, "Reader": deps.Reader, "Preflight": deps.Preflight,
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

func TestDryLine_ShowsTheErrorOrTheResultOnOneLine(t *testing.T) {
	t.Parallel()

	assert.Contains(t, dryLine(1, dbos.StepInfo{StepName: "a", Error: assert.AnError}), "FAILED")
	assert.Equal(t, `step 2 b: map[k:1]`, dryLine(2, dbos.StepInfo{StepName: "b", Output: map[string]int{"k": 1}}))
	assert.Equal(t, "step 3 c: a b", dryLine(3, dbos.StepInfo{StepName: "c", Output: "a\n b"}))
	assert.Equal(t, "step 4 d: "+strings.Repeat("x", dryOutLimit)+"...", dryLine(4, dbos.StepInfo{StepName: "d", Output: strings.Repeat("x", 500)}))
	assert.Equal(t, "step 5 e: FAILED "+strings.Repeat("e", dryOutLimit)+"...", dryLine(5, dbos.StepInfo{StepName: "e", Error: errors.New(strings.Repeat("e", 500))}))
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

func TestDryGate_ASubmitWithABasePointsTheRemoteBranchOfTheBaseAtTheParentOfTheHead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	head, err := dryRepo(t.Context(), dir, t.TempDir())
	require.NoError(t, err)

	gate := newDryGate(dir, head, false)

	job, err := gate.Submit(t.Context(), "p", "i", "", "integration", "", "", model.Slot{})
	require.NoError(t, err)
	assert.Equal(t, "integration", job.Plan.Base)

	base, err := dryGit(t.Context(), dir, []string{"rev-parse", "refs/remotes/origin/integration"})
	require.NoError(t, err)
	parent, err := dryGit(t.Context(), dir, []string{"rev-parse", head + "^"})
	require.NoError(t, err)
	assert.Equal(t, parent, base)

	landing, err := newDryGate(filepath.Join(dir, "none"), head, false).Submit(t.Context(), "p", "i", "", "", "", "", model.Slot{})
	require.NoError(t, err, "a gate with no base touches no repository")
	assert.Empty(t, landing.Plan.Base)

	_, err = newDryGate(dir, "no-such-commit", false).Submit(t.Context(), "p", "i", "", "integration", "", "", model.Slot{})
	require.ErrorContains(t, err, "git exited")
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

	_, err := dryAgents{}.Run(t.Context(), AgentRequest{Prompt: "Judge the change.", OutputFile: file, Outputs: []string{"verdict", "summary", "open"}})
	require.NoError(t, err)

	raw, err := os.ReadFile(file) //nolint:gosec // the test names its own output file
	require.NoError(t, err)
	assert.JSONEq(t, `{"verdict":"approve","summary":"0","open":""}`, string(raw), "a review approves with no open finding, and any other output is 0")
}

func TestDryAgents_TheUncausedOutputIsAnEmptyList(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "out.json")

	_, err := dryAgents{}.Run(t.Context(), AgentRequest{OutputFile: file, Outputs: []string{"report", "uncaused"}})
	require.NoError(t, err)

	raw, err := os.ReadFile(file) //nolint:gosec // the test names its own output file
	require.NoError(t, err)
	assert.JSONEq(t, `{"report":"0","uncaused":"[]"}`, string(raw), "the text 0 is no JSON list, so a dry run would refuse in the filing of the workflow")
}

func TestDryAgents_TheOutputsOfTheReviewLedgerAreEmptyLists(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "out.json")

	_, err := dryAgents{}.Run(t.Context(), AgentRequest{OutputFile: file, Outputs: []string{"findings", "resolved", "wontfix"}})
	require.NoError(t, err)

	raw, err := os.ReadFile(file) //nolint:gosec // the test names its own output file
	require.NoError(t, err)
	assert.JSONEq(t, `{"findings":"[]","resolved":"[]","wontfix":"[]"}`, string(raw), "the text 0 is no JSON list, so a dry run of a loop with a ledger would stop the item")
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
		"local":    {},
		"research": {"RESEARCH_TOOLS": "/tools", "RESEARCH_OUT": "/out"},
	}

	for name, env := range envs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			require.NoError(t, DryRun(t.Context(), filepath.Join("..", "..", "examples", name, "workflow.star"), "12", env, &out), out.String())
		})
	}
}

func TestDryRun_ARoundRunsWithNoEnvironmentVariables(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	// The round reads every setting from the project vars, so the run has no environment at all. It ends early at the read of the queue, which the dry run does not fake.
	// The queue audit judges the rows of that read, so it comes after it and the dry run does not reach it.
	err := DryRun(t.Context(), dryRoundFile, "round/integration", nil, &out, dryRound)

	require.ErrorIs(t, err, ErrDryRunEndedEarly, out.String())
	assert.Contains(t, out.String(), "host_up:round-host#1")
	assert.Contains(t, out.String(), "not run in a dry run: read the open work of the project")
	assert.NotContains(t, out.String(), "agent:queue-audit-1#1")
	assert.NotContains(t, out.String(), "FAILED")
	assert.NotContains(t, out.String(), "env:")
}

func TestDryRun_TheSwarmExampleFilesItsBacklogAndChecksTheStopFileThenEndsEarlyAtTheReadOfTheOpenWork(t *testing.T) {
	t.Parallel()

	env := map[string]string{"SWARM": "s1", "REPO": "o/r", "BASE": "integration", "OWNER_LOGIN": "owner", "OWNER_EMAIL": "owner@example.com"}

	var out bytes.Buffer

	err := DryRun(t.Context(), filepath.Join("..", "..", "examples", "swarm", "workflow.star"), "12", env, &out)
	require.ErrorIs(t, err, ErrDryRunEndedEarly, out.String())

	assert.Contains(t, out.String(), "item_create:create-1")
	assert.Contains(t, out.String(), "command:stop-0")
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
	require.NoError(t, dryWriter{}.SetStatus(ctx, "p", "1", ""))
	require.NoError(t, dryWriter{}.Comment(ctx, "p", "1", "k", "t"))

	comments, err := dryReader{}.Comments(ctx, "p", "1")
	require.NoError(t, err)
	assert.Empty(t, comments)

	parent, err := dryReader{}.Parent(ctx, "p", "1")
	require.NoError(t, err)
	assert.Empty(t, parent)

	id, created, err := dryWriter{}.Create(ctx, "p", "k", trackerstore.NewItem{})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "dry-1", id)

	dir, err := dryTrees{dir: "/t"}.Ensure(ctx, "p", "1", "", "")
	require.NoError(t, err)
	assert.Equal(t, "/t", dir)

	out, err := dryCommands{}.Run(ctx, CommandRequest{})
	require.NoError(t, err)
	assert.Equal(t, CommandOutput{}, out)
}

func TestDryTrees_HeadsReadsTheBranchesOfTheBareRemoteOfTheDryRun(t *testing.T) {
	t.Parallel()

	dir, remote := filepath.Join(t.TempDir(), "tree"), filepath.Join(t.TempDir(), "remote")
	head, err := dryRepo(t.Context(), dir, remote)
	require.NoError(t, err)

	_, err = dryGit(t.Context(), dir, []string{"push", "-q", remoteName, "HEAD:refs/heads/item-1"})
	require.NoError(t, err)

	heads, err := dryTrees{dir: dir}.Heads(t.Context(), "p")

	require.NoError(t, err)
	assert.Contains(t, heads, head+"\trefs/heads/item-1")
}

func countStep(names []string, prefix string) int {
	n := 0

	for _, name := range names {
		if strings.HasPrefix(name, prefix+"#") && strings.HasSuffix(name, "/render") {
			n++
		}
	}

	return n
}

func TestDryRun_WithSendBackTheFirstReviewSendsTheItemToAFixRoundAndTheSecondReviewApproves(t *testing.T) {
	t.Parallel()

	names := reviewStepNames(t, WithSendBack())

	assert.Equal(t, 2, countStep(names, "agent:review"), names)
	assert.Equal(t, 1, countStep(names, "agent:fix"), names)
}

// reviewStepNames dry-runs a small workflow that reviews, fixes on the verdict changes and reviews again, and returns its step names.
func reviewStepNames(t *testing.T, options ...DryOption) []string {
	t.Helper()

	const flow = "def main(item):\n    r = agent(name = \"review\", prompt = \"p.md\", model = \"sonnet\", outputs = [\"verdict\"])\n    if r.verdict == \"changes\":\n        agent(name = \"fix\", prompt = \"p.md\", model = \"sonnet\")\n        agent(name = \"review\", prompt = \"p.md\", model = \"sonnet\", outputs = [\"verdict\"])\n"

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryFileWithPrompt(t, flow), "12", nil, &out, options...), out.String())

	var names []string

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		names = append(names, strings.TrimSuffix(strings.Fields(line)[2], ":"))
	}

	return names
}

func TestDryRun_WithoutSendBackTheReviewApprovesAtOnceAndNoFixRoundRuns(t *testing.T) {
	t.Parallel()

	names := reviewStepNames(t)

	assert.Equal(t, 1, countStep(names, "agent:review"), names)
	assert.Zero(t, countStep(names, "agent:fix"), names)
}

func TestDryRun_TheDryItemCarriesEveryFieldThatAWorkflowCanRead(t *testing.T) {
	t.Parallel()

	fields, err := dryItems{item: "7"}.Fields(context.Background(), "p", "7")
	require.NoError(t, err)

	for _, name := range model.ItemFields() {
		assert.Contains(t, fields, name)
	}
}

func TestDryRun_AProjectWithNoReviewSkillInItsVarsReportsNoSkillAndTheExampleRefusesIt(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	vars := with(roundVars(map[string]any{
		"base": "integration", "owner": "owner", "lane_cap": json.Number("3"), "fix_rounds": json.Number("5"), "label": "beehave-round",
	}), map[string]any{"mutation_command": []any{"sh", "scripts/mutation-pr.sh"}})

	err := DryRun(t.Context(), dryRoundFile, "12", nil, &out, dryPipeline, WithVars(vars))

	require.ErrorContains(t, err, "vars.review_skill")
}

func TestDryRun_ASleepPrintsItsStepAfterTheDuration(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	started := time.Now()

	require.NoError(t, DryRun(t.Context(), dryFile(t, "def main(item):\n    sleep(\"200ms\", name = \"nap\")\n"), "12", map[string]string{}, &out), out.String())
	assert.Contains(t, out.String(), "sleep:nap#1")
	assert.GreaterOrEqual(t, time.Since(started), 200*time.Millisecond)
}

func TestDryRun_ASkillThatTheProjectNamesIsOneOfTheSkillsThatTheFakeAgentReports(t *testing.T) {
	t.Parallel()

	file := dryFile(t, "def main(item):\n    project = facts()\n    a = agent(model = \"m\", prompt = \"a.md\", name = \"a\")\n    if project.vars[\"review_skill\"] not in a.skills:\n        fail(\"REFUSE: the skill did not load\")\n")
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "a.md"), []byte("hello\n"), 0o600))

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), file, "12", map[string]string{}, &out, WithVars(map[string]any{"review_skill": "lint-pass"})), out.String())
	assert.NotContains(t, out.String(), "REFUSE")
}

func TestDryRun_AnImplementRunnerWithNoSkillLineIsRefusedAsARealRunRefusesIt(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	vars := with(roundVars(map[string]any{
		"base": "integration", "owner": "owner", "owner_email": "owner@example.com", "source_checkout": "/own/checkout", "merge": false,
		"lane_cap": json.Number("3"), "fix_rounds": json.Number("5"), "label": "beehave-round",
		"steps": map[string]any{"implement": map[string]any{"runner": "oc"}},
	}), map[string]any{"mutation_command": []any{"sh", "scripts/mutation-pr.sh"}, "review_skill": exampleSkill})

	err := DryRun(t.Context(), dryRoundFile, "12", nil, &out, dryPipeline, WithVars(vars), WithRunnerSkillLoads(map[string]bool{"oc": false}))

	require.ErrorContains(t, err, "runner oc of step implement has no line that loads a skill before the first turn", out.String())
	assert.NotContains(t, out.String(), "agent:", "no agent step ran")
}
