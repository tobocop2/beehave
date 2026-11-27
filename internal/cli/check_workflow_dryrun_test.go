package cli_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent/claude"
	"github.com/tobocop2/beehest/internal/cli"
	"github.com/tobocop2/beehest/internal/config"
)

func runDry(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), append([]string{"check", "workflow", "--dry-run"}, args...), cli.BuildInfo{Registry: registry(), Version: "x"}, env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

// roundProject is a project file whose vars hold the round settings of the remediation example. It sets sandbox = "off" because the test build
// has no sandbox enforcer, which refuses every local gate of a sandboxed project; check_workflow_sandbox_test.go pins that refusal.
const roundProject = "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"g\", test_report = \"r.xml\", sandbox = \"off\", vars = {\"round\": " +
	"{\"base\": \"integration\", \"owner\": \"owner\", \"owner_email\": \"owner@example.com\", \"lane_cap\": 3, \"fix_rounds\": 5, \"label\": \"beehest-round\"}})\n"

func TestCheckWorkflowDryRun_TheRemediationExampleIsARoundThatEndsEarlyAtTheReadOfTheQueueWithNoEnvironment(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "projects/code.star", roundProject)

	code, stdout, stderr := runDry(t, map[string]string{}, "--config", dir, "--project", "code", "--item", "round/integration", filepath.Join("..", "..", "examples", "remediation", "workflow.star"))

	require.Equal(t, 3, code, stderr)
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "host_up:round-host#1", stdout)
	assert.Contains(t, stdout, "not run in a dry run: read the open items of the project")
	assert.NotContains(t, stdout, "agent:queue-audit-1#1", "the queue audit judges the rows of the queue read, so the dry run ends before it")
	assert.NotContains(t, stdout, "FAILED")
	assert.NotContains(t, stdout, "env:")
}

func TestCheckWorkflowDryRun_TheRemediationExampleWithNoRoundInTheProjectVarsIsRefused(t *testing.T) {
	t.Parallel()

	code, stdout, _ := runDry(t, map[string]string{}, "--item", "round/integration", filepath.Join("..", "..", "examples", "remediation", "workflow.star"))

	require.Equal(t, 1, code, stdout)
	assert.Contains(t, stdout, "REFUSE: the project vars hold no round.")
}

func TestCheckWorkflowDryRun_TheDefaultDryRunOfTheRemediationExampleWithATrackerItemWalksThePipeline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "projects/code.star", roundProject)

	_, stdout, stderr := runDry(t, map[string]string{}, "--config", dir, "--project", "code", "--item", "12", filepath.Join("..", "..", "examples", "remediation", "workflow.star"))

	assert.Empty(t, stderr)
	assert.NotContains(t, stdout, "host_up:round-host", "a tracker item runs the pipeline, not a round")
	assert.Contains(t, stdout, "preflight:intake", "the pipeline starts with the intake check of the item")
}

func TestCheckWorkflowDryRun_TheFactsOfANamedProjectHoldItsVars(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    fail(\"V=%r\" % facts().vars)\n")
	writeConfig(t, dir, "projects/code.star", "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"g\", test_report = \"r.xml\", vars = {\"a\": [1]})\n")

	code, stdout, _ := runDry(t, map[string]string{}, "--config", dir, "--project", "code", filepath.Join(dir, "workflow.star"))

	require.Equal(t, 1, code)
	assert.Contains(t, stdout, `V={"a": [1]}`)
}

func TestCheckWorkflowDryRun_ANamedProjectCapsTheDepthOfSpawnAndNoProjectHasTheDefaultCap(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def grand(item):\n    pass\n\ndef child(item):\n    r = gather(spawn(items = [\"22\"], entry = \"grand\")).results[0]\n    if not r.ok:\n        fail(r.error)\n\n"+
		"def main(item):\n    r = gather(spawn(items = [\"21\"], entry = \"child\")).results[0]\n    if not r.ok:\n        fail(r.error)\n")
	writeConfig(t, dir, "projects/code.star", "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"g\", test_report = \"r.xml\", max_spawn_depth = 1)\n")

	code, stdout, _ := runDry(t, map[string]string{}, "--config", dir, "--project", "code", filepath.Join(dir, "workflow.star"))

	require.Equal(t, 1, code)
	assert.Contains(t, stdout, "item 22 would run at depth 2, and the project has a cap of 1.")

	code, stdout, _ = runDry(t, map[string]string{}, filepath.Join(dir, "workflow.star"))

	require.Equal(t, 0, code, stdout)
}

func TestCheckWorkflowDryRun_APlainRunWithoutTheFlagPrintsNoSteps(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", lintAndFixFile)

	code, stdout, _ := runCheckWorkflow(t, filepath.Join(dir, "workflow.star"))

	require.Equal(t, 0, code)
	assert.Empty(t, stdout)
}

func TestCheckWorkflowDryRun_ARedGateInTheFixtureMakesAWorkflowThatStopsAtARedGateExitOneAndTheDefaultExitsZero(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    g = gate(name = \"check\")\n    if not g.passed:\n        fail(\"the gate is red\")\n")
	file := filepath.Join(dir, "workflow.star")

	code, _, _ := runDry(t, map[string]string{}, file)
	require.Equal(t, 0, code)

	writeConfig(t, dir, "red.json", `{"gate:check": [{"passed": false}]}`)

	code, stdout, _ := runDry(t, map[string]string{}, "--fixture", filepath.Join(dir, "red.json"), file)
	require.Equal(t, 1, code)
	assert.Contains(t, stdout, "the gate is red")
}

func TestCheckWorkflowDryRun_AFailureInTheFilePrintsTheErrorAndExitsOne(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    fail(\"REFUSE: not today\")\n")

	code, stdout, _ := runDry(t, map[string]string{}, filepath.Join(dir, "workflow.star"))

	require.Equal(t, 1, code)
	assert.Contains(t, stdout, "error: ")
	assert.Contains(t, stdout, "REFUSE: not today")
	assert.Contains(t, stdout, "\nthe run stopped after ")
}

func TestCheckWorkflowDryRun_AStepThatTheDryRunDoesNotFakeEndsTheRunEarlyAndExitsThree(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    host_teardown(name = \"stop\")\n    fail(\"REFUSE: after the host\")\n")

	code, stdout, stderr := runDry(t, map[string]string{}, filepath.Join(dir, "workflow.star"))

	require.Equal(t, 3, code, stdout+stderr)
	assert.Contains(t, stdout, "not run in a dry run: stop the build host")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(stdout), "the dry run ended early at a step it does not fake: stop the build host; the steps after it were not checked"), stdout)
}

func TestCheckWorkflowDryRun_AFullRunExitsZero(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    pass\n")

	code, stdout, stderr := runDry(t, map[string]string{}, filepath.Join(dir, "workflow.star"))

	require.Equal(t, 0, code, stdout+stderr)
}

func TestCheckWorkflowDryRun_TheHelpTextNamesTheThreeExitCodes(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	cli.Execute(context.Background(), []string{"check", "workflow", "--help"}, cli.BuildInfo{Registry: registry(), Version: "x"}, map[string]string{}, &stdout, &stderr)

	assert.Contains(t, stdout.String(), "Exit 0")
	assert.Contains(t, stdout.String(), "Exit 1")
	assert.Contains(t, stdout.String(), "Exit 3")
}

func TestCheckWorkflowDryRun_TheHelpTextNamesTheWaitAndRecvKeysAndTheTimeOfADryRun(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	cli.Execute(context.Background(), []string{"check", "workflow", "--help"}, cli.BuildInfo{Registry: registry(), Version: "x"}, map[string]string{}, &stdout, &stderr)

	for _, want := range []string{"wait:<label>", "recv:<label>", `"answer"`, `"value"`, "takes no real time"} {
		assert.Contains(t, strings.Join(strings.Fields(stdout.String()), " "), want)
	}
}

func TestCheckWorkflowDryRun_AFileWithProblemsStopsBeforeTheRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def other(item):\n    pass\n")

	code, stdout, stderr := runDry(t, map[string]string{}, filepath.Join(dir, "workflow.star"))

	require.NotEqual(t, 0, code)
	assert.Empty(t, stdout)
	assert.NotEmpty(t, stderr)
}

func TestCheckWorkflowDryRun_RefusesABadItemId(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", lintAndFixFile)

	code, stdout, stderr := runDry(t, map[string]string{}, "--item", "bad id!", filepath.Join(dir, "workflow.star"))

	require.NotEqual(t, 0, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "item id")
}

const reviewThenFix = "def main(item):\n    r = agent(name = \"review\", prompt = \"p.md\", model = \"sonnet\", outputs = [\"verdict\"])\n    if r.verdict == \"changes\":\n        agent(name = \"fix\", prompt = \"p.md\", model = \"sonnet\")\n        agent(name = \"review\", prompt = \"p.md\", model = \"sonnet\", outputs = [\"verdict\"])\n"

func TestCheckWorkflowDryRun_TheFixtureFlagSteersAReviewAndNeedsDryRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "p.md", "Judge the change. Nothing else.")
	writeConfig(t, dir, "workflow.star", reviewThenFix)
	writeConfig(t, dir, "sendback.json", `{"agent:review": [{"outputs": {"verdict": "changes"}}, {"outputs": {"verdict": "approve"}}], "agent:fix": {}}`)
	file := filepath.Join(dir, "workflow.star")

	code, stdout, _ := runDry(t, map[string]string{}, "--item", "12", file)
	require.Equal(t, 0, code, stdout)
	assert.NotContains(t, stdout, "agent:fix")

	code, stdout, _ = runDry(t, map[string]string{}, "--item", "12", "--fixture", filepath.Join(dir, "sendback.json"), file)
	require.Equal(t, 0, code, stdout)
	assert.Contains(t, stdout, "agent:fix")

	code, _, stderr := runCheckWorkflow(t, "--fixture", filepath.Join(dir, "sendback.json"), file)
	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "--dry-run")
}

func TestCheckWorkflowDryRun_AFixtureThatTheRunDoesNotMatchExitsOneAndAFileThatCannotBeReadExitsTwo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "p.md", "Judge the change. Nothing else.")
	writeConfig(t, dir, "workflow.star", reviewThenFix)
	writeConfig(t, dir, "unused.json", `{"agent:review": [{"outputs": {"verdict": "approve"}}, {"outputs": {"verdict": "approve"}}]}`)
	file := filepath.Join(dir, "workflow.star")

	code, stdout, _ := runDry(t, map[string]string{}, "--fixture", filepath.Join(dir, "unused.json"), file)
	require.Equal(t, 1, code)
	assert.Contains(t, stdout, "never made")

	code, _, stderr := runDry(t, map[string]string{}, "--fixture", filepath.Join(dir, "no-such-file.json"), file)
	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "no-such-file.json")
}

func TestCheckWorkflowDryRun_TheOldFlagsAreRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", lintAndFixFile)

	for _, flag := range []string{"--send-back", "--red-gate"} {
		code, _, stderr := runDry(t, map[string]string{}, flag, filepath.Join(dir, "workflow.star"))

		require.NotEqual(t, 0, code, flag)
		assert.Contains(t, stderr, "unknown flag: "+flag)
	}
}

func TestCheckWorkflowDryRun_TheFactsOfANamedProjectHoldTheRunnersOfTheConfigWithTheirSkillLoads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    fail(\"R=%r\" % facts().runners)\n")
	writeConfig(t, dir, "runners.star", "runner(name = \"a\", agent = \"claude\")\nrunner(name = \"o\", agent = \"opencode\")\n")
	writeConfig(t, dir, "projects/code.star", "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"g\", test_report = \"r.xml\")\n")

	adapters := registry()
	adapters.Agents[claude.Name] = &config.Entry{LoadsSkills: true}

	var stdout, stderr bytes.Buffer

	args := []string{"check", "workflow", "--dry-run", "--config", dir, "--project", "code", filepath.Join(dir, "workflow.star")}
	code := cli.Execute(context.Background(), args, cli.BuildInfo{Registry: adapters, Version: "x"}, map[string]string{}, &stdout, &stderr)

	require.Equal(t, 1, code, stderr.String())
	assert.Contains(t, stdout.String(), `R={"a": {"loads_skills": True}, "o": {"loads_skills": False}}`)
}

func TestCheckWorkflowDryRun_AProjectWithTheSandboxOnRefusesASandboxNoneStepAtTheCall(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		key  string
		want int
	}{
		"sandbox on":  {`, sandbox = "on"`, 1},
		"sandbox off": {`, sandbox = "off"`, 0},
		"key unset":   {``, 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			writeConfig(t, dir, "workflow.star", "def main(item):\n    off = None\n    agent(name = \"a\", prompt = \"p.md\", model = \"m\", sandbox = off)\n")
			writeConfig(t, dir, "p.md", "hello\n")
			writeConfig(t, dir, "runners.star", "runner(name = \"a\", agent = \"claude\")\n")
			writeConfig(t, dir, "projects/code.star", "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"g\", test_report = \"r.xml\""+test.key+")\n")

			code, stdout, stderr := runDry(t, map[string]string{}, "--config", dir, "--project", "code", filepath.Join(dir, "workflow.star"))

			assert.Equal(t, test.want, code, stdout+stderr)

			if test.want != 0 {
				assert.Contains(t, stdout, "sets sandbox = None")
			}
		})
	}
}

func TestCheckWorkflowDryRun_AWorkflowThatRefusesARunnerWithNoSkillLineEndsWithExitOneAndTheRefusal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    for name, runner in facts().runners.items():\n        if not runner[\"loads_skills\"]:\n            fail(\"REFUSE: runner %s has no line that loads a skill before the first turn\" % name)\n")
	writeConfig(t, dir, "runners.star", "runner(name = \"a\", agent = \"claude\")\nrunner(name = \"o\", agent = \"opencode\")\n")
	writeConfig(t, dir, "projects/code.star", "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"g\", test_report = \"r.xml\")\n")

	adapters := registry()
	adapters.Agents[claude.Name] = &config.Entry{LoadsSkills: true}

	var stdout, stderr bytes.Buffer

	args := []string{"check", "workflow", "--dry-run", "--config", dir, "--project", "code", filepath.Join(dir, "workflow.star")}
	code := cli.Execute(context.Background(), args, cli.BuildInfo{Registry: adapters, Version: "x"}, map[string]string{}, &stdout, &stderr)

	require.Equal(t, 1, code, stderr.String())
	assert.Contains(t, stdout.String(), "REFUSE: runner o has no line that loads a skill before the first turn")
	assert.NotContains(t, stdout.String(), "runner a has no line")

	withoutRunner := dir + "/without"
	writeConfig(t, withoutRunner, "workflow.star", "def main(item):\n    pass\n")

	code = cli.Execute(context.Background(), []string{"check", "workflow", "--dry-run", filepath.Join(withoutRunner, "workflow.star")}, cli.BuildInfo{Registry: adapters, Version: "x"}, map[string]string{}, &stdout, &stderr)

	require.Equal(t, 0, code, stdout.String())
}

func TestCheckWorkflowDryRun_ACallOfPatchedShowsItsPatchRowAndTakesTheNewBranch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    if patched(\"greeting\"):\n        facts()\n    else:\n        now()\n")

	code, stdout, stderr := runDry(t, map[string]string{}, filepath.Join(dir, "workflow.star"))

	require.Equal(t, 0, code, stdout+stderr)
	assert.Regexp(t, `step \d+ DBOS\.patch-greeting: `, stdout, "the dry run names the patch in a row of its own")
	assert.Contains(t, stdout, "facts#1", "a new run takes the new branch")
	assert.NotContains(t, stdout, "now#1")
}

func TestCheckWorkflowDryRun_AFirstOfOverASpawnedRunReturnsTheRunThatEnded(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def child(item):\n    pass\n\n"+
		"def main(item):\n    e = first_of(runs = spawn(items = [\"21\"], entry = \"child\"))\n    if e.kind != \"run\" or not e.outcome.ok:\n        fail(\"got \" + e.kind)\n")

	code, stdout, _ := runDry(t, map[string]string{}, filepath.Join(dir, "workflow.star"))

	require.Equal(t, 0, code, stdout)
}
