package flow

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keyOf returns the value of the engine key in the environment of a request, and false when the request has none.
func keyOf(request CommandRequest) (string, bool) {
	for _, pair := range request.Env {
		if key, ok := strings.CutPrefix(pair, writeKeyEnv+"="); ok {
			return key, true
		}
	}

	return "", false
}

func TestWrite_TheProgramGetsAnEngineKeyThatIsStableAndDiffersForEachCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	result, err := walkBody(t, h, "a = write(run = [\"push\", \"1\"], name = \"a\")\nb = write(run = [\"push\", \"2\"], name = \"b\")\nprint(a.key)\n")

	require.NoError(t, err)
	require.Len(t, h.commands.requests, 2)

	first, firstOK := keyOf(h.commands.requests[0])
	second, secondOK := keyOf(h.commands.requests[1])

	require.True(t, firstOK && secondOK, "both writes get the key in their environment")
	assert.NotEqual(t, first, second)
	assert.Len(t, first, 32)
	assert.Equal(t, testItem, result.Item)
}

func TestWrite_ThePlainKeyIsTheSameOnAReplayOfTheSameStep(t *testing.T) {
	t.Parallel()

	first := writeKey("run-1", "write:a#1")
	again := writeKey("run-1", "write:a#1")

	assert.Equal(t, first, again, "the same run and step give the same key")
	assert.Len(t, first, 32)
	assert.NotEqual(t, writeKey("run-1", "write:a#1"), writeKey("run-2", "write:a#1"), "another run has its own key")
	assert.NotEqual(t, writeKey("run-1", "write:a#1"), writeKey("run-1", "write:a#2"), "another step has its own key")
}

func TestWrite_AFileCannotSetTheKeyVariableItself(t *testing.T) {
	t.Parallel()

	_, err := walkBody(t, newHarness(t), "write(run = [\"push\"], env = {\""+writeKeyEnv+"\": \"x\"})\n")

	require.ErrorContains(t, err, writeKeyEnv)
}

func TestWrite_AComputedEnvEntryThatIsNoTextIsRefusedByTheRun(t *testing.T) {
	t.Parallel()

	_, err := walkBody(t, newHarness(t), "write(run = [\"push\"], env = {\"A\": 1 + 0})\n")

	require.ErrorContains(t, err, "env must hold strings only")
}

func TestWrite_AReadBackThatReportsDoneStopsTheWriteFromRunning(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "check" {
			return CommandOutput{Stdout: "exists"}, nil
		}

		return CommandOutput{Stdout: "written"}, nil
	}

	_, err := walkBody(t, h, "w = write(run = [\"push\"], read_back = [\"check\"], name = \"w\")\nif not w.skipped or w.stdout != \"exists\":\n    fail(\"the write must be skipped\")\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"check"}, h.commands.ran())

	key, ok := keyOf(h.commands.requests[0])
	assert.True(t, ok, "the read-back gets the key of the write, so it can look the write up")
	assert.NotEmpty(t, key)
}

func TestWrite_AReadBackThatReportsNotDoneLetsTheWriteRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "check" {
			return CommandOutput{RC: 1}, nil
		}

		return CommandOutput{Stdout: "written"}, nil
	}

	_, err := walkBody(t, h, "w = write(run = [\"push\"], read_back = [\"check\"], name = \"w\")\nif w.skipped or w.stdout != \"written\" or w.rc != 0:\n    fail(\"the write must run\")\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"check", "push"}, h.commands.ran())

	read, _ := keyOf(h.commands.requests[0])
	write, _ := keyOf(h.commands.requests[1])
	assert.Equal(t, read, write, "the read-back and the write share one key")
}

func TestWrite_ACrashBetweenTheWriteAndItsRecordDoesNotRunTheWriteAgainWhenTheReadBackFindsIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	written := false
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		switch request.Run[0] {
		case "check":
			if written {
				return CommandOutput{}, nil
			}

			return CommandOutput{RC: 1}, nil
		default:
			written = true

			// The write is done. The run stops before the step records it, as a crash does.
			if err := h.client(t, testVersion).Cancel(testItem); err != nil {
				t.Error(err)
			}

			h.tick(t)

			return CommandOutput{Stdout: "written"}, nil
		}
	}

	file := writeFlow(t, bodyFlow("write(run = [\"push\"], read_back = [\"check\"], name = \"w\")\n"))
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)

	require.Error(t, err)
	require.NoError(t, h.client(t, testVersion).Resume(testItem))

	_, err = engine.RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(strings.Join(h.commands.ran(), ","), "push"), "the write ran once")

	keys := map[string]bool{}

	for _, request := range h.commands.requests {
		key, _ := keyOf(request)
		keys[key] = true
	}

	assert.Len(t, keys, 1, "the replay used the key of the first run")
}

func TestWrite_ACrashBetweenTheWriteAndItsRecordRunsTheWriteAgainWithTheSameKeyWhenThereIsNoReadBack(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	crashed := false
	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		if !crashed {
			crashed = true

			if err := h.client(t, testVersion).Cancel(testItem); err != nil {
				t.Error(err)
			}

			h.tick(t)
		}

		return CommandOutput{}, nil
	}

	file := writeFlow(t, bodyFlow("write(run = [\"push\"], name = \"w\")\n"))
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)

	require.Error(t, err)
	require.NoError(t, h.client(t, testVersion).Resume(testItem))

	_, err = engine.RunFile(testItem, testProject, file)

	require.NoError(t, err)
	require.Len(t, h.commands.requests, 2)

	first, _ := keyOf(h.commands.requests[0])
	second, _ := keyOf(h.commands.requests[1])
	assert.Equal(t, first, second, "the outside system sees the same key and can drop the repeat")
}

const undoFlow = `write(run = ["make", "a"], undo = ["drop", "a"], name = "a")
write(run = ["make", "b"], undo = ["drop", "b"], name = "b")
write(run = ["make", "c"], name = "c")
fail("later step failed")
`

func TestWrite_WhenALaterStepFailsTheUndosRunInReverseOrder(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := walkBody(t, h, undoFlow)

	require.ErrorContains(t, err, "later step failed")
	require.ErrorContains(t, err, "undone: write:b#2, write:a#1")
	assert.Equal(t, []string{"make a", "make b", "make c", "drop b", "drop a"}, h.commands.ran())
}

func TestWrite_AnUndoGetsTheKeyOfItsWrite(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := walkBody(t, h, "write(run = [\"make\"], undo = [\"drop\"], name = \"a\")\nfail(\"x\")\n")

	require.Error(t, err)
	require.Len(t, h.commands.requests, 2)

	made, _ := keyOf(h.commands.requests[0])
	dropped, _ := keyOf(h.commands.requests[1])
	assert.Equal(t, made, dropped)
}

func TestWrite_NoUndoRunsWhenTheRunSucceeds(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := walkBody(t, h, "write(run = [\"make\"], undo = [\"drop\"], name = \"a\")\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"make"}, h.commands.ran())
}

func TestWrite_AWriteThatExitedNonZeroStillGetsItsUndoBecauseTheEngineCannotTellHowMuchHappened(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "make" {
			return CommandOutput{RC: 3}, nil
		}

		return CommandOutput{}, nil
	}

	_, err := walkBody(t, h, "write(run = [\"make\"], undo = [\"drop\"], name = \"a\")\nfail(\"x\")\n")

	require.Error(t, err)
	assert.Equal(t, []string{"make", "drop"}, h.commands.ran())
}

func TestWrite_AWriteSkippedByTheReadBackRegistersNoUndo(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{}, nil }

	_, err := walkBody(t, h, "write(run = [\"push\"], read_back = [\"check\"], undo = [\"drop\"], name = \"a\")\nfail(\"x\")\n")

	require.Error(t, err)
	assert.Equal(t, []string{"check"}, h.commands.ran(), "the write was done by someone else, so the run does not undo it")
}

func TestWrite_ACrashDuringTheUndosResumesThemAndDoesNotRepeatTheFinishedOnes(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	crashed := false
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if strings.Join(request.Run, " ") == "drop a" && !crashed {
			crashed = true

			if err := h.client(t, testVersion).Cancel(testItem); err != nil {
				t.Error(err)
			}

			h.tick(t)
		}

		return CommandOutput{}, nil
	}

	file := writeFlow(t, bodyFlow(undoFlow))
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)

	require.Error(t, err)
	require.NoError(t, h.client(t, testVersion).Resume(testItem))

	_, err = engine.RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "later step failed")

	ran := h.commands.ran()
	assert.Equal(t, 1, count(ran, "drop b"), "the finished undo did not run again")
	assert.Equal(t, 1, count(ran, "make a"))
	assert.Equal(t, 2, count(ran, "drop a"), "the cut-off undo ran again with its key")
}

func TestWrite_ABadReadBackOrUndoIsRefusedBeforeAnythingRuns(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ call, want string }{
		"read_back not a list": {`write(run = ["a"], read_back = "x")`, "read_back"},
		"empty undo":           {`write(run = ["a"], undo = [])`, "undo must name a program"},
		"empty read_back":      {`write(run = ["a"], read_back = [])`, "read_back must name a program"},
		"bad timeout":          {`write(run = ["a"], read_back = ["b"], timeout = "soon")`, "timeout"},
		"bad run":              {`write(run = "a")`, "run"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			_, err := walkBody(t, h, test.call+"\n")

			require.ErrorContains(t, err, test.want)
			assert.Empty(t, h.commands.requests)
		})
	}
}

func TestWrite_AProgramThatCannotStartFailsTheStepAtTheReadBackOrTheWriteOrTheUndo(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ fail, call string }{
		"read-back": {"check", `write(run = ["push"], read_back = ["check"])`},
		"write":     {"push", `write(run = ["push"])`},
		"undo":      {"drop", "write(run = [\"push\"], undo = [\"drop\"])\nfail(\"later\")"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.commands.before = func(_ context.Context, request CommandRequest) error {
				if request.Run[0] == test.fail {
					return errFake
				}

				return nil
			}

			_, err := walkBody(t, h, test.call+"\n")

			require.ErrorContains(t, err, errFake.Error())
		})
	}
}

func TestWrite_AnUndoThatExitsNonZeroFailsTheRunWithItsError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "drop" {
			return CommandOutput{RC: 4, Stderr: "cannot drop"}, nil
		}

		return CommandOutput{}, nil
	}

	_, err := walkBody(t, h, "write(run = [\"push\"], undo = [\"drop\"], name = \"w\")\nfail(\"later\")\n")

	require.ErrorContains(t, err, "later")
	require.ErrorContains(t, err, "undo of write:w#1")
	require.ErrorContains(t, err, "exited with code 4. cannot drop")
}

func count(list []string, want string) int {
	return len(slices.DeleteFunc(slices.Clone(list), func(s string) bool { return s != want }))
}
