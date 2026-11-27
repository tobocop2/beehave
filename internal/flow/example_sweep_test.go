package flow

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host"
	"github.com/tobocop2/beehest/internal/model"
)

// These tests run the sweep step of the example: after a lane ends, the build host runs the clean command of the project and no other command.

// sweepOf reads the text that the child run returns as its sweep result.
func sweepOf(t *testing.T, result ItemResult) string {
	t.Helper()

	var value struct {
		Sweep string `json:"sweep"`
	}

	require.NoError(t, json.Unmarshal(result.Value, &value))

	return value.Sweep
}

// sweepRequests returns the host requests of the run that clean, in order.
func sweepRequests(rig *agentRig) []CommandRequest {
	rig.h.commands.mu.Lock()
	defer rig.h.commands.mu.Unlock()

	var found []CommandRequest

	for _, request := range rig.h.commands.requests {
		if request.Host == "build" && request.SlotClass == model.DefaultClass && len(request.Run) > 0 && request.Run[0] == "clean-lane" {
			found = append(found, request)
		}
	}

	return found
}

// dockerRequests returns every command request of the run that starts the program docker.
func dockerRequests(rig *agentRig) []CommandRequest {
	rig.h.commands.mu.Lock()
	defer rig.h.commands.mu.Unlock()

	var found []CommandRequest

	for _, request := range rig.h.commands.requests {
		if len(request.Run) > 0 && request.Run[0] == "docker" {
			found = append(found, request)
		}
	}

	return found
}

func TestExample_ALaneEndsWithTheSweepOfItsTargetDirectory(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["clean_command"] = []any{"clean-lane", "--all"}

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved)

	runs := sweepRequests(rig)
	require.Len(t, runs, 1, "one clean and nothing else")
	assert.Equal(t, []string{"clean-lane", "--all"}, runs[0].Run, "the clean command runs as the project wrote it")

	ran := rig.h.commands.ran()
	assert.Equal(t, "clean-lane --all", ran[len(ran)-1], "the sweep is the last work of the lane")
	assert.Equal(t, "clean: rc=0", sweepOf(t, result))
	assert.Empty(t, dockerRequests(rig), "the sweep runs no docker command")
}

func TestExample_ALaneWithNoCleanCommandRunsNoCommandAndSaysSo(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.NotEmpty(t, rig.h.commands.ran(), "the lane ran host commands, so the empty lists below read a live recorder")
	assert.Empty(t, sweepRequests(rig))
	assert.Empty(t, dockerRequests(rig), "no command of the lane starts docker")
	assert.Equal(t, "clean: not run, vars.clean_command is not set", sweepOf(t, result))
}

func TestExample_AFailedCleanShowsItsExitCodeAndDoesNotFailTheLane(t *testing.T) {
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
	assert.Equal(t, "clean: rc=3", sweepOf(t, result))
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
	assert.Contains(t, sweepOf(t, result), "clean: rc=-1 error: the build host is gone")
}

func TestExample_ALaneThatTheReviewRejectsIsSweptToo(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "reject" })
	rig.h.writer = newFakeWriter()
	rig.project.Vars["clean_command"] = []any{"clean-lane"}

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, "rejected", childOf(t, result).Reason)
	assert.Len(t, sweepRequests(rig), 1, "the sweep does not depend on the verdict")
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
	assert.Len(t, sweepRequests(rig), 1, "the sweep ran before the error went up")
}
