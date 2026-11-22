package gate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// localCleanPath is the PATH of the engine at its start in these tests.
const localCleanPath = "/usr/bin:/bin:/opt/homebrew/bin"

// localProgramGate is a gate whose shell runs the command with a real sh and whose tree reads the real item folder.
// The engine resolved the tools of the job on a clean PATH, as it does at its start.
func localProgramGate(t *testing.T, dir string) Gate {
	t.Helper()

	return localProgramGateOn(t, dir, localCleanPath, localTools(t))
}

// localTools finds the tools of the job in localCleanPath, as the engine does at its start.
func localTools(t *testing.T) Tools {
	t.Helper()

	find := func(names ...string) string {
		for _, name := range names {
			for _, dir := range filepath.SplitList(localCleanPath) {
				if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
					return filepath.Join(dir, name)
				}
			}
		}

		require.FailNow(t, "tool not found", names)

		return ""
	}

	return Tools{Git: find("git"), Find: find("find"), Sort: find("sort"), Readlink: find("readlink"), Cat: find("cat"), Digest: find("sha256sum", "shasum")}
}

// localProgramGateOn is localProgramGate for a job that runs with the PATH jobPath and the given tools.
func localProgramGateOn(t *testing.T, dir, jobPath string, tools Tools) Gate {
	t.Helper()

	shell := func(ctx context.Context, where, command string) (string, int) {
		cmd := exec.CommandContext(ctx, "sh", "-c", command) //nolint:gosec // the job text under test
		cmd.Dir = where
		cmd.Env = []string{"PATH=" + jobPath, "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()

		if err == nil {
			return string(out), 0
		}

		var exit *exec.ExitError

		require.ErrorAs(t, err, &exit, string(out))

		return string(out), exit.ExitCode()
	}
	read := func(ctx context.Context, args ...string) string {
		cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // test helper
		cmd.Dir = dir
		out, err := cmd.Output()
		require.NoError(t, err)

		return strings.TrimSpace(string(out))
	}
	tree := func(ctx context.Context, _ string) (State, error) {
		return State{Head: read(ctx, "rev-parse", "HEAD"), Tree: read(ctx, "rev-parse", "HEAD^{tree}"), Clean: read(ctx, "status", "--porcelain") == ""}, nil
	}

	return New(shell, tree, nil).WithTools(tools)
}

// localItem makes the item worktree at the head of the case, as the engine does for an item. It returns the folder and the request of the gate.
func localItem(t *testing.T, c programCase) (string, Request) {
	t.Helper()

	repo, head, tip := programRepo(t, c)
	item := filepath.Join(t.TempDir(), "item")
	shIn(t, repo, "git worktree add -q --detach "+item+" "+head)

	project := programProjectOf()
	project.BuildHost = config.LocalHost

	return item, Request{Project: project, Dir: item, CloneURL: repo, BaseSHA: tip}
}

func TestRun_ALocalGateWithAGateProgramJudgesTheHeadWithTheGateProgramOfTheBase(t *testing.T) {
	t.Parallel()

	for _, c := range programCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			item, request := localItem(t, c)
			result, err := localProgramGate(t, item).Run(context.Background(), request)
			require.NoError(t, err)

			if c.pass {
				require.True(t, result.Passed, result.Report)

				return
			}

			require.False(t, result.Passed, result.Report)

			if rest, program := strings.CutPrefix(c.reason, "gate-program "); program {
				assert.Contains(t, result.Report, rest)
				assert.Contains(t, result.Steps, Step{Name: model.StepGateProgram, Status: StatusFail})
			} else {
				assert.Contains(t, result.Report, c.reason)
			}
		})
	}
}

func TestRun_ALocalGateWithAGateProgramRunsTheHeadCodeInACopyAndNotInTheItemTree(t *testing.T) {
	t.Parallel()

	item, request := localItem(t, programCase{files: map[string]string{"code.sh": "printf x > ran-in-the-copy.txt\n"}})
	result, err := localProgramGate(t, item).Run(context.Background(), request)
	require.NoError(t, err)
	require.True(t, result.Passed, result.Report)

	assert.NoFileExists(t, filepath.Join(item, "ran-in-the-copy.txt"))
	assert.NoDirExists(t, filepath.Join(item, "reports"))
}

func TestRun_ALocalGateWithAGateProgramAndNoBaseCommitRunsNoJob(t *testing.T) {
	t.Parallel()

	item, request := localItem(t, programCase{files: map[string]string{"tests_new.txt": "x\n"}})
	request.BaseSHA = ""

	_, err := localProgramGate(t, item).Run(context.Background(), request)
	require.ErrorIs(t, err, ErrNoBase)
}

func TestRun_ALocalGateWithAGateProgramGivesTheJobTheItemFolderAndAFolderThatItRemoves(t *testing.T) {
	t.Parallel()

	item, request := localItem(t, programCase{files: map[string]string{"tests_new.txt": "x\n"}})

	var from, root string

	g := localProgramGate(t, item).WithProgramShell(func(_ context.Context, source, made, command string) (string, int) {
		from, root = source, made

		assert.DirExists(t, made)
		assert.Contains(t, command, `"$gpbin" clone --no-local -- `+item)

		return "", 0
	})

	result, err := g.Run(context.Background(), request)
	require.NoError(t, err)
	require.True(t, result.Passed)

	assert.Equal(t, item, from)
	assert.NoDirExists(t, root)
}

func TestRun_ALocalGateWithAGateProgramThatCannotMakeItsFolderIsAnError(t *testing.T) {
	item, request := localItem(t, programCase{files: map[string]string{"tests_new.txt": "x\n"}})
	t.Setenv("TMPDIR", filepath.Join(item, "no", "such", "folder"))

	_, err := localProgramGate(t, item).Run(context.Background(), request)
	require.ErrorContains(t, err, "make the folder of the gate copy")
}

func TestRun_ALocalGateWithAGateProgramReadsItsJobResultFromTheExitCodeAndTheLog(t *testing.T) {
	t.Parallel()

	steps := model.Project{Name: "demo", GateProgram: []string{"Makefile"}, GateSteps: []model.GateSpec{{Name: "lint", Command: "make lint"}, {Name: "unit", Command: "make unit"}}}
	cases := []struct {
		name    string
		project model.Project
		out     string
		code    int
		passed  bool
		report  string
		steps   []Step
	}{
		{name: "exit_zero_passes", project: programProjectOf(), passed: true, steps: []Step{{Name: StepCommand, Status: StatusPass}, {Name: StepTests, Status: StatusPass}}},
		{name: "a_signal_gives_no_result", project: programProjectOf(), code: 137, report: "killed by a signal", steps: []Step{{Name: StepCommand, Status: StatusUnknown}, {Name: StepTests, Status: StatusUnknown}}},
		{name: "the_time_limit_gives_no_result", project: programProjectOf(), code: ExitTimedOut, report: "time limit", steps: []Step{{Name: StepCommand, Status: StatusUnknown}, {Name: StepTests, Status: StatusUnknown}}},
		{name: "a_refusal_line_names_the_step", project: programProjectOf(), code: 1, out: programMarker + " the head changed the gate program, paths Makefile\n", report: "paths Makefile", steps: []Step{{Name: StepCommand, Status: StatusUnknown}, {Name: StepTests, Status: StatusUnknown}, {Name: model.StepGateProgram, Status: StatusFail}}},
		{name: "a_refusal_report_holds_the_log_of_the_job", project: programProjectOf(), code: 1, out: "fatal: unable to access a file\n" + programMarker + " the base commit cannot be read\n", report: "fatal: unable to access a file", steps: []Step{{Name: StepCommand, Status: StatusUnknown}, {Name: StepTests, Status: StatusUnknown}, {Name: model.StepGateProgram, Status: StatusFail}}},
		{name: "a_marker_inside_a_word_is_no_refusal", project: programProjectOf(), code: 1, out: "x" + programMarker + " the head changed the gate program, paths Makefile\n", report: "xbeehave-gate-program", steps: []Step{{Name: StepCommand, Status: StatusFail}, {Name: StepTests, Status: StatusUnknown}}},
		{name: "the_first_of_two_refusal_lines_gives_the_reason", project: programProjectOf(), code: 1, out: programMarker + " first reason\n" + programMarker + " second reason\n", report: "first reason", steps: []Step{{Name: StepCommand, Status: StatusUnknown}, {Name: StepTests, Status: StatusUnknown}, {Name: model.StepGateProgram, Status: StatusFail}}},
		{name: "a_failed_gate_string_fails_with_its_output", project: programProjectOf(), code: 2, out: "boom\n", report: "boom", steps: []Step{{Name: StepCommand, Status: StatusFail}, {Name: StepTests, Status: StatusUnknown}}},
		{name: "a_failed_list_reads_the_step_lines", project: steps, code: 1, out: stepMarker + " lint rc=0\n" + stepMarker + " unit rc=2\n", report: stepMarker, steps: []Step{{Name: "lint", Status: StatusPass}, {Name: "unit", Status: StatusFail}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			result, err := localVerdict(context.Background(), Request{Project: c.project}, "head", c.out, c.code)
			require.NoError(t, err)

			assert.Equal(t, c.passed, result.Passed)
			assert.Equal(t, "head", result.Head)
			assert.Contains(t, result.Report, c.report)
			assert.Equal(t, c.steps, result.Steps)
		})
	}
}

func TestRun_ALocalGateWithAGateProgramEndsWithTheRunWhenAStoppedRunKilledTheJob(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := localVerdict(ctx, Request{Project: programProjectOf()}, "head", "", 143)
	require.ErrorIs(t, err, context.Canceled)
}
