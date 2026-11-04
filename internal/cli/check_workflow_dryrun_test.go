package cli_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/cli"
)

func runDry(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), append([]string{"check", "workflow", "--dry-run"}, args...), cli.BuildInfo{Version: "x"}, env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestCheckWorkflowDryRun_PrintsEachStepOfTheRemediationExampleInOrder(t *testing.T) {
	t.Parallel()

	env := map[string]string{"REPO": "o/r", "BASE": "integration", "OWNER_LOGIN": "owner", "OWNER_EMAIL": "owner@example.com", "SOURCE_CHECKOUT": "/own/checkout", "MERGE": "false", "EXPECT_HEAD": ""}

	code, stdout, stderr := runDry(t, env, "--item", "12", filepath.Join("..", "..", "examples", "remediation", "workflow.star"))

	require.Equal(t, 0, code, stderr)
	assert.Empty(t, stderr)
	assert.Less(t, strings.Index(stdout, "agent:plan#1"), strings.Index(stdout, "agent:implement#2"), stdout)
	assert.Contains(t, stdout, "gate:check#1")
}

func TestCheckWorkflowDryRun_APlainRunWithoutTheFlagPrintsNoSteps(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", lintAndFixFile)

	code, stdout, _ := runCheckWorkflow(t, filepath.Join(dir, "workflow.star"))

	require.Equal(t, 0, code)
	assert.Empty(t, stdout)
}

func TestCheckWorkflowDryRun_TheRedGateFlagMakesAWorkflowThatStopsAtARedGateExitOneAndTheDefaultExitsZero(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    g = gate(name = \"check\")\n    if not g.passed:\n        fail(\"the gate is red\")\n")
	file := filepath.Join(dir, "workflow.star")

	code, _, _ := runDry(t, map[string]string{}, file)
	require.Equal(t, 0, code)

	code, stdout, _ := runDry(t, map[string]string{}, "--red-gate", file)
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

	cli.Execute(context.Background(), []string{"check", "workflow", "--help"}, cli.BuildInfo{Version: "x"}, map[string]string{}, &stdout, &stderr)

	assert.Contains(t, stdout.String(), "Exit 0")
	assert.Contains(t, stdout.String(), "Exit 1")
	assert.Contains(t, stdout.String(), "Exit 3")
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

func TestCheckWorkflowDryRun_TheSendBackFlagWalksAFixRoundAfterTheFirstReview(t *testing.T) {
	t.Parallel()

	env := map[string]string{"REPO": "o/r", "BASE": "integration", "OWNER_LOGIN": "owner", "OWNER_EMAIL": "owner@example.com", "SOURCE_CHECKOUT": "/own/checkout", "MERGE": "false", "EXPECT_HEAD": ""}
	file := filepath.Join("..", "..", "examples", "remediation", "workflow.star")

	code, stdout, _ := runDry(t, env, "--item", "12", file)
	require.Equal(t, 0, code, stdout)
	assert.NotContains(t, stdout, "agent:fix")

	code, stdout, _ = runDry(t, env, "--item", "12", "--send-back", file)
	require.Equal(t, 0, code, stdout)
	assert.Contains(t, stdout, "agent:fix")
}
