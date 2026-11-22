package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run the round steps of the example: the integration pull request, the cut of the next round and the record of the base reds.

// commandsNamed returns the command lines of the run whose program is the first word.
func commandsNamed(rig *roundRig, first string) []string {
	var lines []string

	for _, line := range rig.h.commands.ran() {
		if strings.HasPrefix(line, first+" ") || line == first {
			lines = append(lines, line)
		}
	}

	return lines
}

// integrationState makes the integration tool answer with the pull request 9 in the state.
func integrationState(rig *roundRig, state string) {
	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 1 && request.Run[1] == "integration" {
			return CommandOutput{Stdout: "pr=9 state=" + state + "\n"}, nil
		}

		return previous(request)
	}
}

func TestRound_TheRoundAsksTheToolForTheIntegrationPullRequestFromTheRoundBaseIntoTheTrunk(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.setRound("base", "integration")
	rig.setRound("integration_pr", json.Number("9"))

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"remedy integration --base integration --into main --pr 9"}, commandsNamed(rig, "remedy integration"))
}

func TestRound_AnIntegrationLineWithNoPullRequestNumberStopsTheRun(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 1 && request.Run[1] == "integration" {
			return CommandOutput{Stdout: "garbage\n"}, nil
		}

		return previous(request)
	}

	_, err := rig.run(t)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no `pr=<n>` figure")
}

func TestRound_TheNextRoundIsCutFromTheOldBaseAndTheIntegrationPullRequestOnceItMerged(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.setRound("base", "integration")
	rig.setRound("next_base", "round-v")
	rig.setRound("integration_pr", json.Number("9"))
	rig.answer = "CONTINUE"
	integrationState(rig, "MERGED")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Contains(t, commandsNamed(rig, "remedy"), "remedy cut --old integration --new round-v --pr 9 --trunk main")
}

// vars.round.integration_pr 0 lets the tool find the integration pull request, so the cut takes the number that the tool printed.
func TestRound_ARoundWithNoIntegrationNumberCutsWithTheNumberThatTheToolFound(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.setRound("base", "integration")
	rig.setRound("next_base", "round-v")
	rig.answer = "CONTINUE"
	integrationState(rig, "MERGED")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Contains(t, commandsNamed(rig, "remedy"), "remedy cut --old integration --new round-v --pr 9 --trunk main")
}

func TestRound_AStopFromTheOwnerCutsNoNextRoundEvenWhenTheIntegrationPullRequestMerged(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.setRound("next_base", "round-v")
	integrationState(rig, "MERGED")

	_, err := rig.run(t)

	require.NoError(t, err)

	for _, line := range commandsNamed(rig, "remedy") {
		assert.NotContains(t, line, "remedy cut")
	}
}

func TestRound_TheCutOfTheNextRoundThatTheToolRefusesEndsTheRunWithItsReason(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.setRound("next_base", "round-v")
	rig.answer = "CONTINUE"
	integrationState(rig, "MERGED")

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 1 && request.Run[1] == "cut" {
			return CommandOutput{RC: 2, Stdout: "REFUSE: pull request 9 is not merged"}, nil
		}

		return previous(request)
	}

	result, err := rig.run(t)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Contains(t, result.Stopped.Output, "is not merged")
}

func TestRound_TheBaseRedsAreOneRecordWithTheTimeAndTheTip(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		file any
		want string
	}{
		"the default file is named by the round and lies outside its folder": {nil, "BASE_REDS_FILE=../base-reds-main.md"},
		"a file of the settings is the file of the record":                   {"reds.md", "BASE_REDS_FILE=reds.md"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t, "")
			rig.setRound("base_reds", []any{"lint-a", "test-b"})

			if test.file != nil {
				rig.setRound("base_reds_file", test.file)
			}

			previous := rig.h.commands.reply
			rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
				if request.Run[0] == "git" {
					return CommandOutput{Stdout: "abc123\n"}, nil
				}

				return previous(request)
			}

			_, err := rig.run(t)
			require.NoError(t, err)

			var writes []CommandRequest

			for _, request := range rig.h.commands.requests {
				if slices.ContainsFunc(request.Env, func(pair string) bool { return strings.HasPrefix(pair, "BASE_REDS_FILE=") }) {
					writes = append(writes, request)
				}
			}

			require.Len(t, writes, 1, "the record is written once, by one program")

			written := writes[0]
			assert.Equal(t, []string{"sh", "-c", `umask 022 && exec tee -- "$BASE_REDS_FILE"`}, written.Run)
			assert.Contains(t, written.Env, test.want, "the path goes in the environment of the program")
			assert.Equal(t, time.Minute, written.Timeout)
			assert.Contains(t, written.Stdin, "tip abc123 of main")
			assert.Contains(t, written.Stdin, "Measured at 20")
			assert.Contains(t, written.Stdin, "- lint-a\n- test-b\n")
			assert.Equal(t, 1, strings.Count(written.Stdin, "# Known base reds"), "one record, not a list of records")
		})
	}
}

func TestRound_ANoChildIsOpenBeforeTheRoundReadsTheIntegrationPullRequestAndWaits(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)

	require.NotEmpty(t, commandsNamed(rig, "remedy integration"), "the round reached its integration read")
	assert.Equal(t, []string{"1", "3"}, rig.kids.started())
	assert.ElementsMatch(t, rig.kids.started(), rig.kids.reviewedAtIntegration(), "every child had ended its review before the round asks the owner")
}

func TestRound_TheWaitTimeoutIsTheRoundWaitHours(t *testing.T) {
	t.Parallel()

	for _, hours := range []int{24, 3} {
		rig := newRoundRig(t, "")
		rig.setRound("wait_hours", json.Number(strconv.Itoa(hours)))
		start := time.Now()

		_, err := rig.run(t)

		require.NoError(t, err)
		require.Len(t, rig.deadlines, 1, "the round asked the owner once")
		assert.WithinDuration(t, start.Add(time.Duration(hours)*time.Hour), rig.deadlines[0], time.Minute, "the wait of the round ends after vars.round.wait_hours")
	}
}

// waitOverEngine is the engine of the round rig with a clock that moves one hour at each reading, so a wait of one hour is over at its second reading.
// The example file stays as it is.
func waitOverEngine(t *testing.T, rig *roundRig) *Engine {
	t.Helper()

	start := time.Now()

	var readings atomic.Int64

	return rig.engineWith(t, func(deps *Deps) {
		deps.Clock.Reading = func() time.Time { return start.Add(time.Duration(readings.Add(1)) * time.Hour) }
		deps.Clock.OwnerWait = 10 * time.Millisecond
	})
}

// roundValue is what the entry round returns: the state of the round, its base and its integration pull request.
type roundValue struct {
	State string `json:"state"`
	Base  string `json:"base"`
	PR    int    `json:"pr"`
}

func TestRound_ATimeoutReturnsTheStateWaitingWithTheBaseAndThePullRequestAndCutsNoNextRound(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.setRound("wait_hours", json.Number("1"))
	rig.setRound("next_base", "round-v")
	integrationState(rig, "MERGED")

	engine := waitOverEngine(t, rig)

	// The entry round keeps its value in the result, which main drops.
	in, err := pinnedInput(testItem, testProject, rig.file, modeOff())
	require.NoError(t, err)

	in.Entry = "round"
	result, err := engine.run(in)

	require.NoError(t, err)
	assert.Nil(t, result.Stopped)

	var value roundValue

	require.NoError(t, json.Unmarshal(result.Value, &value))
	assert.Equal(t, roundValue{State: "waiting", Base: "main", PR: 9}, value)
	assert.NotContains(t, strings.Join(stepNames(t, engine), " "), "queue:round-queue-2#", "a timeout ends the round and starts no second pass")

	for _, line := range commandsNamed(rig, "remedy") {
		assert.NotContains(t, line, "remedy cut", "a timeout is a STOP and cuts nothing")
	}
}

func TestRound_AContinueStartsAnotherPassThatAsksTheQueueAgain(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.answers = []string{"CONTINUE", "STOP"}

	_, err := rig.run(t)

	require.NoError(t, err)

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.Contains(t, joined, "queue:round-queue-1#")
	assert.Contains(t, joined, "queue:round-queue-2#", "the answer CONTINUE starts the second pass")
	assert.NotContains(t, joined, "queue:round-queue-3#", "the answer STOP after it ends the round")
}

func TestRound_TheFileOfTheExampleReadsNoEnvironmentVariable(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile(filepath.Join("..", "..", "examples", "remediation", "workflow.star"))
	require.NoError(t, err)

	assert.NotContains(t, string(text), "env(", "every setting of the round comes from the project file")
}

func TestRound_ARoundWhoseItemBaseIsNotItsBaseSettingSpawnsNoChildAndNamesBothBases(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	_, err := rig.engine(t).RunFile(testRound, testProject, rig.file, modeOff())

	require.ErrorContains(t, err, "the round item "+testRound+" has the base integration/s1, and the base given to spawn is main")
	assert.Empty(t, rig.kids.started(), "no child started")
}
