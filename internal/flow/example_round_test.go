package flow

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run the round steps of the example file: the record of a merge, the cut of the next round and the record of the base reds.

// recountReply answers the recount tool with the rows and issues before the write, and a pass after it.
func recountReply(rows, issues string) func(CommandRequest) (CommandOutput, error) {
	return func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 1 && request.Run[1] == "recount" && !slices.Contains(request.Run, "--was-rows") {
			return CommandOutput{Stdout: "rows=" + rows + " issues=" + issues + "\n"}, nil
		}

		return CommandOutput{}, nil
	}
}

func mergeRig(t *testing.T, closes string) (*agentRig, string) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.opts.Env["ROUND_STEP"] = "merge"
	rig.h.opts.Env["MERGED_PR"] = "41"
	rig.h.opts.Env["INTEGRATION_PR"] = "9"
	rig.h.opts.Env["CLOSES"] = closes
	rig.h.commands.reply = recountReply("4", "2")

	return rig, file
}

func TestExampleRound_AMergeAddsTheRowThenTheAgentWritesThenTheCountMustHaveMoved(t *testing.T) {
	t.Parallel()

	rig, file := mergeRig(t, "")

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"remedy recount --pr 9",
		"remedy recount --pr 9 --was-rows 4",
	}, rig.h.commands.ran(), "the count is read before the agent and asserted after it, and a merge with no issue asserts no issue count")

	require.Len(t, rig.h.agents.seen(), 1)
	assert.Contains(t, rig.h.agents.seen()[0].Prompt, "Add the integration row")
	assert.Contains(t, rig.h.agents.seen()[0].Prompt, "The pull request 41 ")
}

func TestExampleRound_AMergeThatResolvesAnIssueAssertsTheIssueCountToo(t *testing.T) {
	t.Parallel()

	rig, file := mergeRig(t, "12 13")

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	assert.Equal(t, "remedy recount --pr 9 --was-rows 4 --was-issues 2", rig.h.commands.ran()[1])
	assert.Contains(t, rig.h.agents.seen()[0].Prompt, "The issues are: 12 13.")
}

func TestExampleRound_ACountThatDidNotChangeStopsTheRun(t *testing.T) {
	t.Parallel()

	rig, file := mergeRig(t, "")
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if slices.Contains(request.Run, "--was-rows") {
			return CommandOutput{RC: 1, Stdout: "rows=4 issues=2\nthe row count is still 4: the write did not land\n"}, nil
		}

		return recountReply("4", "2")(request)
	}

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, 1, result.Stopped.RC)
	assert.Contains(t, result.Stopped.Output, "the write did not land")
}

func TestExampleRound_ABeforeLineWithNoFigureFailsClosedAndNamesTheMissingLine(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ closes, stdout, missing string }{
		"no rows figure":   {"", "garbage\n", "rows="},
		"no issues figure": {"12", "rows=4\n", "issues="},
		"not a number":     {"", "rows=x issues=2\n", "rows="},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, file := mergeRig(t, tc.closes)
			rig.h.commands.reply = func(CommandRequest) (CommandOutput, error) {
				return CommandOutput{Stdout: tc.stdout}, nil
			}

			_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "recount-before")
			assert.Contains(t, err.Error(), tc.missing)
			assert.Equal(t, []string{"remedy recount --pr 9"}, rig.h.commands.ran(), "no assert runs with -1, and none is skipped")
			assert.Empty(t, rig.h.agents.seen(), "the agent does not run on an unknown count")
		})
	}
}

func TestExampleRound_ABeforeCountWithAFindingStillLetsTheAgentFixIt(t *testing.T) {
	t.Parallel()

	rig, file := mergeRig(t, "")
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if !slices.Contains(request.Run, "--was-rows") {
			return CommandOutput{RC: 1, Stdout: "rows=4 issues=2\nheading says 3 but the section has 4 rows\n"}, nil
		}

		return CommandOutput{}, nil
	}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 1, "exit code 1 before the agent is the defect that the agent fixes")
	assert.Equal(t, "remedy recount --pr 9 --was-rows 4", rig.h.commands.ran()[1])
}

func TestExampleRound_ARoundThatTracksNoIntegrationPullRequestRecordsNothing(t *testing.T) {
	t.Parallel()

	rig, file := mergeRig(t, "")
	rig.h.opts.Env["INTEGRATION_PR"] = "none"

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	assert.Empty(t, rig.h.commands.ran())
	assert.Empty(t, rig.h.agents.seen())
}

func TestExampleRound_TheNextRoundIsCutFromTheOldBaseAndTheIntegrationPullRequest(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.opts.Env["ROUND_STEP"] = "cut"
	rig.h.opts.Env["NEXT_BASE"] = "round-v"
	rig.h.opts.Env["INTEGRATION_PR"] = "9"

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	assert.Equal(t, []string{"remedy cut --old integration --new round-v --pr 9 --trunk main"}, rig.h.commands.ran())
	assert.Empty(t, rig.h.agents.seen())
}

func TestExampleRound_TheCutOfTheNextRoundThatTheToolRefusesEndsTheRunWithItsReason(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.opts.Env["ROUND_STEP"] = "cut"
	rig.h.opts.Env["NEXT_BASE"] = "round-v"
	rig.h.opts.Env["INTEGRATION_PR"] = "9"
	rig.h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{RC: 2, Stdout: "REFUSE: pull request 9 is not merged"}, nil
	}

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Contains(t, result.Stopped.Output, "is not merged")
}

func TestExampleRound_TheBaseRedsAreOneRecordWithTheTimeAndTheTip(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.opts.Env["ROUND_STEP"] = "reds"
	rig.h.opts.Env["BASE_REDS"] = "lint-a test-b"
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "git" {
			return CommandOutput{Stdout: "abc123\n"}, nil
		}

		return CommandOutput{}, nil
	}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	var written CommandRequest

	for _, request := range rig.h.commands.requests {
		if request.Run[0] == "tee" {
			written = request
		}
	}

	require.NotEmpty(t, written.Run, "the record is written by one program")
	assert.Equal(t, []string{"tee", "base-reds.md"}, written.Run)
	assert.Contains(t, written.Stdin, "tip abc123 of integration")
	assert.Contains(t, written.Stdin, "Measured at 20")
	assert.Contains(t, written.Stdin, "- lint-a\n- test-b\n")
	assert.Equal(t, 1, strings.Count(written.Stdin, "# Known base reds"), "one record, not a list of records")
}
