package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/model"
)

func runCLI(t *testing.T, env map[string]string, info cli.BuildInfo, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	if env == nil {
		env = map[string]string{}
	}

	if env["OBEE_STATE_DIR"] == "" {
		env["OBEE_STATE_DIR"] = filepath.Join(t.TempDir(), "state")
	}

	code := cli.Execute(context.Background(), args, info, env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func writeFile(t *testing.T, name, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func TestLedger_AddThenCountThroughTheTree(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	env := map[string]string{"OBEE_STATE_DIR": state}

	var stdout, stderr bytes.Buffer

	add := cli.Execute(context.Background(), []string{"ledger", "add", "12", "lane-a", "fix", "--mechanism", "m1"}, cli.BuildInfo{}, env, &stdout, &stderr)
	require.Equal(t, 0, add, stderr.String())

	stdout.Reset()
	count := cli.Execute(context.Background(), []string{"ledger", "count", "12", "fix"}, cli.BuildInfo{}, env, &stdout, &stderr)
	require.Equal(t, 0, count, stderr.String())
	require.Equal(t, "1\n", stdout.String())

	stdout.Reset()
	prior := cli.Execute(context.Background(), []string{"ledger", "prior", "12", "lane-b"}, cli.BuildInfo{}, env, &stdout, &stderr)
	require.Equal(t, 0, prior, stderr.String())
	require.Equal(t, "1\n", stdout.String())

	stdout.Reset()
	rounds := cli.Execute(context.Background(), []string{"ledger", "rounds", "12", "m1"}, cli.BuildInfo{}, env, &stdout, &stderr)
	require.Equal(t, 0, rounds, stderr.String())
	require.Contains(t, stdout.String(), "1")

	stdout.Reset()
	closes := cli.Execute(context.Background(), []string{"ledger", "closes", "12"}, cli.BuildInfo{}, env, &stdout, &stderr)
	require.Equal(t, 0, closes, stderr.String())
}

func TestLedger_ARefusedKindExitsTwo(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCLI(t, nil, cli.BuildInfo{}, "ledger", "add", "12", "lane-a", "nonsense")

	require.Equal(t, 2, code)
	require.NotEmpty(t, stderr)
}

func TestBriefNew_RefusesAMissingKeyBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	rules := writeFile(t, "rules.md", "# Rules\n\n## Maintainer preflight\n\nnone measured yet\n\n## CI checks and their base\n\n- Run: make check\n- Base: origin/main\n\n## Standing rules\n\n- Take: none\n")

	code, stdout, stderr := runCLI(t, nil, cli.BuildInfo{}, "brief", "new", "impl", "lane-a", "sonnet", "--rules", rules)

	require.Equal(t, 2, code)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "no PR number")
}

func TestBriefNew_RefusesAnUnknownKind(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCLI(t, nil, cli.BuildInfo{}, "brief", "new", "bogus", "lane-a", "sonnet", "--key", "1")

	require.Equal(t, 2, code)
	require.NotEmpty(t, stderr)
}

func TestBriefCheck_AMissingFileExitsTwo(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCLI(t, nil, cli.BuildInfo{}, "brief", "check", filepath.Join(t.TempDir(), "absent.md"))

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "no brief at")
}

func TestBriefSize_ReportsAFileAndExitsZero(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "brief.md", "# Lane\n\nRead first:\n")

	code, stdout, stderr := runCLI(t, nil, cli.BuildInfo{}, "brief", "size", path)

	require.Equal(t, 0, code, stderr)
	require.NotEmpty(t, stdout)
}

func TestBriefSize_AMissingFileExitsTwo(t *testing.T) {
	t.Parallel()

	code, _, _ := runCLI(t, nil, cli.BuildInfo{}, "brief", "size", filepath.Join(t.TempDir(), "absent.md"))

	require.Equal(t, 2, code)
}

func TestCheckAuthors_RefusesWithoutAnAddressAndExitsTwo(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "authors.txt", "abc1234 a@b.c someone\n")

	code, _, stderr := runCLI(t, nil, cli.BuildInfo{}, "check", "authors", path)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "ME_EMAIL")
}

func TestCheckAuthors_AMatchingAuthorExitsZero(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "authors.txt", "abc1234 me@x.io me\n")

	code, _, stderr := runCLI(t, map[string]string{"ME_EMAIL": "me@x.io", "ME_LOGIN": "me"}, cli.BuildInfo{}, "check", "authors", path)

	require.Equal(t, 0, code, stderr)
}

func TestCheckAuthors_AMissingFileExitsTwo(t *testing.T) {
	t.Parallel()

	code, _, _ := runCLI(t, nil, cli.BuildInfo{}, "check", "authors", filepath.Join(t.TempDir(), "absent"))

	require.Equal(t, 2, code)
}

func TestCheckReport_AnEmptyReportFailsWithOne(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "report.md", "nothing here\n")

	code, stdout, _ := runCLI(t, nil, cli.BuildInfo{}, "check", "report", path, "--head", "abc")

	require.Equal(t, 1, code)
	require.NotEmpty(t, stdout)
}

func TestCheckHandoff_AHandoffWithNoSpendIsRefusedWithOne(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "handoff.md", "# Handoff\n")

	code, stdout, _ := runCLI(t, nil, cli.BuildInfo{}, "check", "handoff", path)

	require.Equal(t, 1, code)
	require.Contains(t, stdout, "REFUSED")
}

func TestCheckJob_AMissingScriptExitsTwo(t *testing.T) {
	t.Parallel()

	list := writeFile(t, "list.md", "1. make check\n")

	code, _, _ := runCLI(t, nil, cli.BuildInfo{}, "check", "job", "--script", filepath.Join(t.TempDir(), "absent.sh"), "--list", list)

	require.Equal(t, 2, code)
}

func TestCheckJob_AScriptThatMissesAStepFailsWithOne(t *testing.T) {
	t.Parallel()

	script := writeFile(t, "job.sh", "#!/bin/sh\necho hi\n")
	list := writeFile(t, "list.md", "## Gate steps\nDerived from: ci.yaml at 1a2b3c4d\n| name | command |\n|---|---|\n| fmt | `cargo fmt --check` |\n")

	code, stdout, _ := runCLI(t, nil, cli.BuildInfo{}, "check", "job", "--script", script, "--list", list)

	require.Equal(t, 1, code)
	require.NotEmpty(t, stdout)
}

func skillCalls(calls []model.Call, err error) func(string, string) ([]model.Call, error) {
	return func(string, string) ([]model.Call, error) { return calls, err }
}

func TestCheckSkills_ACalledSkillExitsZero(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{Skills: skillCalls([]model.Call{{Line: 1, Text: "blast-radius-review"}}, nil)}}

	code, _, stderr := runCLI(t, nil, info, "check", "skills", "t.jsonl", "--skill", "blast-radius-review")

	require.Equal(t, 0, code, stderr)
}

func TestCheckSkills_AMissingSkillExitsOne(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{Skills: skillCalls(nil, nil)}}

	code, stdout, _ := runCLI(t, nil, info, "check", "skills", "t.jsonl", "--skill", "blast-radius-review")

	require.Equal(t, 1, code)
	require.NotEmpty(t, stdout)
}

func TestCheckSkills_AFailedReadExitsTwo(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{Skills: skillCalls(nil, errors.New("no file"))}}

	code, _, stderr := runCLI(t, nil, info, "check", "skills", "t.jsonl", "--skill", "x")

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "no file")
}

func TestCheckSkills_WithoutAReaderTheCommandIsAbsent(t *testing.T) {
	t.Parallel()

	code, stdout, _ := runCLI(t, nil, cli.BuildInfo{}, "check", "--help")

	require.Equal(t, 0, code)
	require.NotContains(t, stdout, "skills")
}
