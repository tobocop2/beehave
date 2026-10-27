package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/model"
)

func gitOutput(stdout string, exit int) func(context.Context, []string) model.CommandResult {
	return func(context.Context, []string) model.CommandResult {
		return model.CommandResult{Stdout: stdout, ExitCode: exit}
	}
}

func TestCheckPapers_ARangeWithOnlyDocumentsExitsZero(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{Git: gitOutput("README.md\n", 0)}}

	code, stdout, stderr := runCLI(t, nil, info, "check", "papers", "a..b")

	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "OK")
}

func TestCheckPapers_ASourcePathExitsTwo(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{Git: gitOutput("main.go\n", 0)}}

	code, stdout, _ := runCLI(t, nil, info, "check", "papers", "a..b")

	require.Equal(t, 2, code)
	require.Contains(t, stdout, "REFUSED")
}

func TestCheckPapers_ABadRangeExitsTwo(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{Git: gitOutput("", 0)}}

	code, _, stderr := runCLI(t, nil, info, "check", "papers", "nodots")

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "usage")
}

func TestCheckPapers_AFailedGitExitsTwo(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{Git: gitOutput("", 128)}}

	code, _, stderr := runCLI(t, nil, info, "check", "papers", "a..b")

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "git cannot read")
}

func scopeDeps(closes []string, closesErr error, body string) cli.CheckDeps {
	return cli.CheckDeps{
		Closes:    func(string) ([]string, error) { return closes, closesErr },
		OwnerAdds: func(string) ([]string, error) { return nil, nil },
		PRBody:    func(context.Context, string, int) (string, error) { return body, nil },
	}
}

func TestCheckScope_AnUnchangedScopeExitsZero(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: scopeDeps([]string{"1"}, nil, "Closes #1")}

	code, _, stderr := runCLI(t, nil, info, "check", "scope", "7")

	require.Equal(t, 0, code, stderr)
}

func TestCheckScope_ANewIssueExitsThree(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: scopeDeps([]string{"1"}, nil, "Closes #1\nCloses #2")}

	code, stdout, _ := runCLI(t, nil, info, "check", "scope", "7")

	require.Equal(t, 3, code)
	require.NotEmpty(t, stdout)
}

func TestCheckScope_ANonNumberExitsTwo(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: scopeDeps(nil, nil, "")}

	code, _, stderr := runCLI(t, nil, info, "check", "scope", "seven")

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "digits only")
}

func TestCheckScope_AFailedLedgerReadExitsTwo(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: scopeDeps(nil, errors.New("ledger down"), "")}

	code, _, stderr := runCLI(t, nil, info, "check", "scope", "7")

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "ledger down")
}

func bashCalls(calls []model.Call, err error) func(string) ([]model.Call, error) {
	return func(string) ([]model.Call, error) { return calls, err }
}

func TestCheckTranscript_ACleanTranscriptExitsZero(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{Bash: bashCalls([]model.Call{{Line: 1, Text: "git status"}}, nil)}}

	code, _, stderr := runCLI(t, nil, info, "check", "transcript", "t.jsonl")

	require.Equal(t, 0, code, stderr)
}

func TestCheckTranscript_AForbiddenCommandExitsOne(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{Bash: bashCalls([]model.Call{{Line: 4, Text: "git add -A"}}, nil)}}

	code, stdout, _ := runCLI(t, nil, info, "check", "transcript", "t.jsonl")

	require.Equal(t, 1, code)
	require.Contains(t, stdout, "HIT line 4")
}

func TestCheckTranscript_AFailedReadExitsTwo(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{Bash: bashCalls(nil, errors.New("no file"))}}

	code, _, stderr := runCLI(t, nil, info, "check", "transcript", "t.jsonl")

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "no file")
}
