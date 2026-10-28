package cli_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
)

const lintAndFixFile = `workflow "lint-and-fix" {
  method_version = "lint-1"
}

loop "tidy" {
  max   = 3
  until = command.lint.rc == 0

  command "lint" {
    run = ["golangci-lint", "run"]
  }

  agent "fix" {
    prompt = "prompts/fix.md"
    model  = loop.round <= 2 ? "sonnet" : "opus"
    when   = command.lint.rc != 0
  }
}
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
	writeConfig(t, dir, "workflow.hcl", lintAndFixFile)
	writeConfig(t, dir, "prompts/fix.md", "Fix {{ .lint }}\n")

	code, stdout, stderr := runCheckWorkflow(t, filepath.Join(dir, "workflow.hcl"))

	require.Equal(t, 0, code)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
}

func TestCheckWorkflow_ReadsWorkflowHclInTheCurrentFolderByDefault(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "workflow.hcl", lintAndFixFile)
	writeConfig(t, dir, "prompts/fix.md", "Fix\n")
	t.Chdir(dir)

	code, _, stderr := runCheckWorkflow(t)

	require.Equal(t, 0, code)
	require.Empty(t, stderr)
}

func TestCheckWorkflow_PrintsEveryProblemInPositionOrderAndExitsOne(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "w.hcl")
	writeConfig(t, dir, "w.hcl", strings.Replace(strings.Replace(lintAndFixFile, "command.lint.rc != 0", "itme.id", 1), "run = [\"golangci-lint\", \"run\"]", "run = [agent.fix.text]", 1))

	code, stdout, stderr := runCheckWorkflow(t, file)

	require.Equal(t, 1, code)
	require.Empty(t, stdout)

	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	require.Len(t, lines, 4, stderr)
	require.True(t, strings.HasPrefix(lines[0], file+":10:12: "), lines[0])
	require.Contains(t, lines[0], "`agent.fix` runs later")
	require.True(t, strings.HasPrefix(lines[1], file+":14:14: "), lines[1])
	require.Contains(t, lines[1], "prompt file `prompts/fix.md` is missing")
	require.True(t, strings.HasPrefix(lines[2], file+":16:14: "), lines[2])
	require.Contains(t, lines[2], "unknown name `itme`")
	require.Equal(t, "3 problems in "+file, lines[3])
}

func TestCheckWorkflow_SaysOneProblemInTheSingular(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "w.hcl")
	writeConfig(t, dir, "w.hcl", "workflow \"w\" {}\ncommand \"a\" {\n  run = [command.a.rc]\n}\n")

	code, _, stderr := runCheckWorkflow(t, file)

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "cycle `a -> a`")
	require.True(t, strings.HasSuffix(stderr, "1 problem in "+file+"\n"), stderr)
}

func TestCheckWorkflow_ExitsOneForAStructuralProblem(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "w.hcl")
	writeConfig(t, dir, "w.hcl", "workflow \"w\" {}\nagent \"a\" {\n  bogus = 1\n}\n")

	code, _, stderr := runCheckWorkflow(t, file)

	require.Equal(t, 1, code)
	require.Contains(t, stderr, file+":3:3: ")
}

func TestCheckWorkflow_ExitsTwoForAFileThatCannotBeRead(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "none.hcl")

	code, _, stderr := runCheckWorkflow(t, file)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, file+": cannot read the file")
	require.Contains(t, stderr, "1 problem in "+file)
}

func TestCheckWorkflow_ExitsTwoForASyntaxError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "w.hcl")
	writeConfig(t, dir, "w.hcl", "workflow \"w\" {\n  x = \n}\n")

	code, _, stderr := runCheckWorkflow(t, file)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, file+":2:")
}

func TestCheckWorkflow_RejectsTwoFiles(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCheckWorkflow(t, "a.hcl", "b.hcl")

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "accepts at most 1 arg")
}
