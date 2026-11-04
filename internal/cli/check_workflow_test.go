package cli_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/cli"
)

const lintAndFixFile = `def main(item):
    lint = command(name = "lint", run = ["golangci-lint", "run"])
    if lint.rc != 0:
        agent(name = "fix", prompt = "prompts/fix.md", model = "sonnet")
`

func runCheckWorkflow(t *testing.T, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), append([]string{"check", "workflow"}, args...), cli.BuildInfo{Version: "x"}, map[string]string{}, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestCheckWorkflow_ExitsZeroForTheLintAndFixWorkflow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", lintAndFixFile)

	code, stdout, stderr := runCheckWorkflow(t, filepath.Join(dir, "workflow.star"))

	require.Equal(t, 0, code)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
}

func TestCheckWorkflow_ReadsWorkflowStarInTheCurrentFolderByDefault(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", lintAndFixFile)
	t.Chdir(dir)

	code, _, stderr := runCheckWorkflow(t)

	require.Equal(t, 0, code)
	require.Empty(t, stderr)
}

func TestCheckWorkflow_PrintsEveryProblemInPositionOrderAndExitsOne(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "w.star")
	writeConfig(t, dir, "w.star", "def main(item):\n    command(name = \"a\", run = [itme.id])\n    gate(name = \"g\", run = [other])\n")

	code, stdout, stderr := runCheckWorkflow(t, file)

	require.Equal(t, 2, code, "an undefined name stops the file from loading")
	require.Empty(t, stdout)

	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	require.Len(t, lines, 3, stderr)
	require.True(t, strings.HasPrefix(lines[0], file+":2:"), lines[0])
	require.Contains(t, lines[0], "itme")
	require.True(t, strings.HasPrefix(lines[1], file+":3:"), lines[1])
	require.Contains(t, lines[1], "other")
	require.Equal(t, "2 problems in "+file, lines[2])
}

func TestCheckWorkflow_SaysOneProblemInTheSingular(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "w.star")
	writeConfig(t, dir, "w.star", "x = 1\n")

	code, _, stderr := runCheckWorkflow(t, file)

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "the file defines no function main")
	require.True(t, strings.HasSuffix(stderr, "1 problem in "+file+"\n"), stderr)
}

func TestCheckWorkflow_ExitsTwoForAFileThatCannotBeRead(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "none.star")

	code, _, stderr := runCheckWorkflow(t, file)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, file+": cannot read")
	require.Contains(t, stderr, "1 problem in "+file)
}

func TestCheckWorkflow_ExitsTwoForASyntaxError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "w.star")
	writeConfig(t, dir, "w.star", "def main(item:\n")

	code, _, stderr := runCheckWorkflow(t, file)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, file+":1:")
}

func TestCheckWorkflow_RejectsTwoFiles(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCheckWorkflow(t, "a.star", "b.star")

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "accepts at most 1 arg")
}
