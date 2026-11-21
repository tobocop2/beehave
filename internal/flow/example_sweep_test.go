package flow

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/host"
)

// These tests run the sweep step of the example: after a lane ends, the build host runs the clean command of the project and prunes the Docker volumes.

const pruneLine = "docker volume prune -f"

// sweepOf reads the text that the child run returns as its sweep result.
func sweepOf(t *testing.T, result ItemResult) string {
	t.Helper()

	var value struct {
		Sweep string `json:"sweep"`
	}

	require.NoError(t, json.Unmarshal(result.Value, &value))

	return value.Sweep
}

// sweepRequests returns the host requests of the run that clean or prune, in order.
func sweepRequests(rig *agentRig) []CommandRequest {
	rig.h.commands.mu.Lock()
	defer rig.h.commands.mu.Unlock()

	var found []CommandRequest

	for _, request := range rig.h.commands.requests {
		if request.Host == "build" && request.SlotClass == "" && len(request.Run) > 0 && (request.Run[0] == "clean-lane" || slices.Equal(request.Run, []string{"docker", "volume", "prune", "-f"})) {
			found = append(found, request)
		}
	}

	return found
}

func TestExample_ALaneEndsWithTheSweepOfItsTargetDirectoryAndTheDockerVolumes(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["clean_command"] = []any{"clean-lane", "--all"}

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved)

	runs := sweepRequests(rig)
	require.Len(t, runs, 2, "one clean and one prune")
	assert.Equal(t, []string{"clean-lane", "--all"}, runs[0].Run, "the clean command comes first, as the project wrote it")
	assert.Equal(t, []string{"docker", "volume", "prune", "-f"}, runs[1].Run)

	ran := rig.h.commands.ran()
	assert.Equal(t, "clean-lane --all", ran[len(ran)-2], "the sweep is the last work of the lane")
	assert.Equal(t, pruneLine, ran[len(ran)-1])
	assert.Contains(t, sweepOf(t, result), "clean: rc=0")
	assert.Contains(t, sweepOf(t, result), "prune: rc=0")
}

func TestExample_ALaneWithNoCleanCommandRunsOnlyThePruneAndSaysSo(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)

	runs := sweepRequests(rig)
	require.Len(t, runs, 1)
	assert.Equal(t, []string{"docker", "volume", "prune", "-f"}, runs[0].Run)
	assert.Contains(t, sweepOf(t, result), "clean: not run, vars.clean_command is not set")
	assert.Contains(t, sweepOf(t, result), "prune: rc=0")
}

func TestExample_AFailedCleanShowsItsExitCodeAndDoesNotFailTheLaneOrSkipThePrune(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["clean_command"] = []any{"clean-lane"}

	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 0 && request.Run[0] == "clean-lane" {
			return CommandOutput{RC: 3, Stdout: "no space"}, nil
		}

		return CommandOutput{}, nil
	}

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved, "a failed sweep does not fail the lane")
	assert.Len(t, sweepRequests(rig), 2, "the prune runs after the failed clean")
	assert.Contains(t, sweepOf(t, result), "clean: rc=3")
	assert.Contains(t, sweepOf(t, result), "prune: rc=0")
}

func TestExample_ASweepStepThatCannotRunShowsItsErrorAndDoesNotFailTheLane(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["clean_command"] = []any{"clean-lane"}

	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 0 && request.Run[0] == "clean-lane" {
			return CommandOutput{}, fmt.Errorf("the build host is gone: %w", host.ErrLost)
		}

		return CommandOutput{}, nil
	}

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved, "an error of the sweep does not fail the lane")
	assert.Len(t, sweepRequests(rig), 2, "the prune runs after the clean that could not run")
	assert.Contains(t, sweepOf(t, result), "clean: rc=-1 error: the build host is gone")
	assert.Contains(t, sweepOf(t, result), "prune: rc=0")
}

func TestExample_APruneThatLosesTheHostShowsItsErrorAndDoesNotFailTheLane(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 0 && request.Run[0] == "docker" {
			return CommandOutput{}, fmt.Errorf("the build host is gone: %w", host.ErrLost)
		}

		return CommandOutput{}, nil
	}

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved, "a host lost at the prune does not fail the lane")
	assert.Contains(t, sweepOf(t, result), "prune: rc=-1 error: the build host is gone")
}

func TestExample_AFailedPruneShowsItsExitCode(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 0 && request.Run[0] == "docker" {
			return CommandOutput{RC: 1}, nil
		}

		return CommandOutput{}, nil
	}

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved)
	assert.Contains(t, sweepOf(t, result), "prune: rc=1")
}

func TestExample_ALaneThatTheReviewRejectsIsSweptToo(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "reject" })
	rig.h.writer = newFakeWriter()
	rig.project.Vars["clean_command"] = []any{"clean-lane"}

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, "rejected", childOf(t, result).Reason)
	assert.Len(t, sweepRequests(rig), 2, "the sweep does not depend on the verdict")
}

func TestExample_ACleanCommandThatIsNotAListOfWordsIsRefusedBeforeAnyAgentStarts(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]any{
		"a text":        "clean-lane",
		"an empty list": []any{},
		"a blank word":  []any{"clean-lane", " "},
		"a number":      []any{json.Number("3")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, file := exampleRig(t, func(int) string { return "approve" })
			rig.project.Vars["clean_command"] = value

			_, err := runPipeline(rig.h.engine(t), file)

			require.ErrorContains(t, err, "REFUSE: vars.clean_command ")
			assert.Empty(t, rig.h.agents.requests, "no agent started")
		})
	}
}

func TestExample_ALaneThatEndsWithTheRefusalOfASkippedSkillIsSweptBeforeTheErrorGoesUp(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["clean_command"] = []any{"clean-lane"}

	reporting := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := reporting(request)
		result.Skills = nil

		return result
	}

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, "did not call the skill "+exampleSkill, "the lane still fails with its own error")
	assert.Len(t, sweepRequests(rig), 2, "the sweep ran before the error went up")
}

func TestExample_ALaneThatEndsWithAMissingChangelogEntryIsSweptBeforeTheErrorGoesUp(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["clean_command"] = []any{"clean-lane"}
	rig.project.Vars["changelog"] = "CHANGELOG.md"

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 1 && request.Run[1] == "diff" {
			return CommandOutput{Stdout: "one.txt\n"}, nil
		}

		if previous != nil {
			return previous(request)
		}

		return CommandOutput{}, nil
	}

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, "the project declares the changelog CHANGELOG.md", "the lane still fails with its own error")
	assert.Len(t, sweepRequests(rig), 2, "the sweep ran before the error went up")
}
