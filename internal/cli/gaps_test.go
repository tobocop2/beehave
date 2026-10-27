package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/ledger"
)

// runIn runs the command tree with the given state folder and returns the exit code and both streams.
func runIn(t *testing.T, state string, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	env := map[string]string{"OBEE_STATE_DIR": state}
	code := cli.Execute(context.Background(), args, cli.BuildInfo{}, env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

var ledgerCommands = map[string][]string{
	"add":    {"ledger", "add", "12", "lane-a", "fix"},
	"count":  {"ledger", "count", "12", "fix"},
	"rounds": {"ledger", "rounds", "12", "m1"},
	"prior":  {"ledger", "prior", "12", "lane-a"},
	"closes": {"ledger", "closes", "12"},
	"brief":  {"brief", "new", "impl", "lane-a", "sonnet", "--key", "12"},
}

func TestLedgerCommands_ARelativeStateFolderExitsTwo(t *testing.T) {
	t.Parallel()

	for name, args := range ledgerCommands {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code, _, stderr := runIn(t, "relative/state", args...)

			require.Equal(t, 2, code)
			require.Contains(t, stderr, "not an absolute path")
		})
	}
}

func TestLedgerCommands_AFailedReadExitsTwo(t *testing.T) {
	t.Parallel()

	for name, args := range ledgerCommands {
		if name == "brief" {
			continue
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			state := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(state, ledger.FileName), 0o700))
			require.NoError(t, os.Mkdir(filepath.Join(state, ledger.FileName+".closes"), 0o700))

			code, _, stderr := runIn(t, state, args...)

			require.Equal(t, 2, code)
			require.NotEmpty(t, stderr)
		})
	}
}

func TestLedgerAdd_ARefusedBodyFileExitsTwo(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "absent.md")

	code, _, stderr := runIn(t, t.TempDir(), "ledger", "add", "12", "lane-r", "review", "--body-file", missing)

	require.Equal(t, 2, code)
	require.NotEmpty(t, stderr)
}

func TestLedgerAdd_AFirstReviewListsTheIssuesItCloses(t *testing.T) {
	t.Parallel()

	body := writeFile(t, "body.md", "Closes #7\n")

	code, stdout, stderr := runIn(t, t.TempDir(), "ledger", "add", "12", "lane-r", "review", "--body-file", body)

	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "7")
}

func TestBriefNew_AnUnknownLaneKindExitsTwo(t *testing.T) {
	t.Parallel()

	code, _, stderr := runIn(t, t.TempDir(), "brief", "new", "impl", "lane-a", "sonnet", "--key", "12", "--lane-kind", "bogus")

	require.Equal(t, 2, code)
	require.NotEmpty(t, stderr)
}

func TestBriefNew_AHandoffThatSpendsNothingStopsTheSuccessor(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	code, _, stderr := runIn(t, state, "ledger", "add", "12", "lane-a", "fix", "--mechanism", "m1")
	require.Equal(t, 0, code, stderr)

	handoff := writeFile(t, "handoff.md", "# Handoff\n")
	rules := writeFile(t, "rules.md", "# Rules\n\n## Maintainer preflight\n\nnone\n\n## CI checks and their base\n\n- Run: make check\n- Base: origin/main\n\n## Standing rules\n\n- Take: none\n")

	code, stdout, stderr := runIn(t, state, "brief", "new", "impl", "lane-b", "sonnet", "--key", "12", "--mechanism", "m2", "--rules", rules, "--handoff", handoff, "--acceptance", "none: no output", "--base-result", "none: no output")

	require.Equal(t, 2, code)
	require.Contains(t, stdout+stderr, "REFUSED", stdout+stderr)
}

func TestBriefNew_AFirstReviewReadsTheBodyFileAndWritesTheOutFile(t *testing.T) {
	t.Parallel()

	rules := writeFile(t, "rules.md", "# Rules\n\n## Maintainer preflight\n\nnone\n\n## CI checks and their base\n\n- Run: make check\n- Base: origin/main\n\n## Standing rules\n\n- Take: none\n")
	body := writeFile(t, "body.md", "Closes #7\n")
	out := filepath.Join(t.TempDir(), "brief.md")

	code, _, stderr := runIn(t, t.TempDir(), "brief", "new", "rev", "lane-r", "sonnet", "--key", "12", "--review-pr", "12",
		"--rules", rules, "--body-file", body, "--out", out)

	require.Equal(t, 0, code, stderr)
	require.FileExists(t, out)
	require.Contains(t, stderr, "first review")

	bad := filepath.Join(t.TempDir(), "absent", "brief.md")
	code, _, stderr = runIn(t, t.TempDir(), "brief", "new", "rev", "lane-r", "sonnet", "--key", "12", "--review-pr", "12",
		"--rules", rules, "--body-file", body, "--out", bad)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "write")
}

func TestCheckCommands_AMissingFileExitsTwo(t *testing.T) {
	t.Parallel()

	absent := filepath.Join(t.TempDir(), "absent.md")
	other := writeFile(t, "list.md", "1. make check\n")

	cases := map[string][]string{
		"report":      {"check", "report", absent},
		"handoff":     {"check", "handoff", absent},
		"job script":  {"check", "job", "--script", absent, "--list", other},
		"job list":    {"check", "job", "--script", other, "--list", absent},
		"job success": nil,
	}

	for name, args := range cases {
		if args == nil {
			continue
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code, _, stderr := runIn(t, t.TempDir(), args...)

			require.Equal(t, 2, code)
			require.NotEmpty(t, stderr)
		})
	}
}

func TestCheckHandoff_ACapAndATranscriptReachTheCheck(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "handoff.md", "# Handoff\n")
	transcript := writeFile(t, "t.jsonl", "")

	code, stdout, stderr := runIn(t, t.TempDir(), "check", "handoff", path, "--cap", "50000", "--transcript", transcript)

	require.Equal(t, 2, code, stdout+stderr)
	require.Contains(t, stdout, "REFUSED")
}

const (
	gapsHead   = "abcdef1234567890abcdef1234567890abcdef12"
	gapsReport = "# Report\n\n## Claims\n| Claim | Where | Evidence | Verified at |\n|---|---|---|---|\n" +
		"| refuses an empty name | body | test empty_name_refused, red at base | " + gapsHead + " |\n\n" +
		"## Hunks\n| Hunk | Arm | Red test line |\n|---|---|---|\n" +
		"| src/a.rs:10-14 | handler never calls validate() | test empty_name_refused ... FAILED |\n\n" +
		"## Inputs\n| Input class | Base | Head | Test |\n|---|---|---|---|\n" +
		"| empty string | accepted | refused 400 | empty_name_refused |\n\n" +
		"## Siblings\n| Site | Same behavior | Updated |\n|---|---|---|\n" +
		"| src/b.rs:40 create route | validates the same name | yes |\n\n" +
		"no background commands left\n"
	gapsList = "# Gate list: owner/name\n\n## Gate steps\nDerived from: `.github/workflows/ci.yaml`, jobs `lint` and `test`, at 1a2b3c4d.\n\n" +
		"| name | command |\n|---|---|\n| fmt | `make fmt` |\n"
)

func TestCheckReport_AReportThatPassesExitsZero(t *testing.T) {
	t.Parallel()

	report := writeFile(t, "report.md", gapsReport)

	code, stdout, stderr := runIn(t, t.TempDir(), "check", "report", report, "--head", gapsHead)

	require.Equal(t, 0, code, stdout+stderr)
	require.Contains(t, stdout, "check-report: OK")
}

func TestCheckJob_AJobThatRunsEveryStepExitsZeroAndReadsASourcedFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "env.sh"), []byte("reds=0\n"), 0o600))

	script := filepath.Join(dir, "job.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/bash\nsource env.sh\nmake fmt; echo \"STEP_RESULT name=fmt rc=$?\"\nreds=$?\necho \"DONE-x rc=$reds gate=full\"\n"), 0o600))

	list := writeFile(t, "list.md", gapsList)

	code, stdout, stderr := runIn(t, t.TempDir(), "check", "job", "--script", script, "--list", list)

	require.Equal(t, 0, code, stdout+stderr)
	require.Contains(t, stdout, "check-job: OK")
}

func TestCheckJob_AListWithNoGateStepsExitsTwo(t *testing.T) {
	t.Parallel()

	script := writeFile(t, "job.sh", "#!/bin/sh\necho hi\n")
	list := writeFile(t, "list.md", "1. make check\n")

	code, _, stderr := runIn(t, t.TempDir(), "check", "job", "--script", script, "--list", list)

	require.Equal(t, 2, code)
	require.NotEmpty(t, stderr)
}

func TestBriefNew_PrintsTheBriefToStdoutWithoutAnOutFile(t *testing.T) {
	t.Parallel()

	rules := writeFile(t, "rules.md", "# Rules\n\n## Maintainer preflight\n\nnone\n\n## CI checks and their base\n\n- Run: make check\n- Base: origin/main\n\n## Standing rules\n\n- Take: none\n")
	body := writeFile(t, "body.md", "No issue.\n")

	code, stdout, stderr := runIn(t, t.TempDir(), "brief", "new", "rev", "lane-r", "sonnet", "--key", "12", "--review-pr", "12",
		"--rules", rules, "--body-file", body)

	require.Equal(t, 0, code, stderr)
	require.NotEmpty(t, stdout)
}

func TestHostSubmit_RefusesAProjectTheConfigDoesNotName(t *testing.T) {
	t.Parallel()

	info, env, dir := hostRig(t, &fakeMachine{})

	code, _, stderr := runCLI(t, env, info, "host", "submit", "other", "make", "--config", dir, "--log", "l", "--marker", "m")

	require.Equal(t, 2, code)
	require.Contains(t, stderr, `no project named "other"`)
}
