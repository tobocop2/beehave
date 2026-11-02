package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/obee/internal/workflow"
)

func TestBuiltins_ABadCallIsRefusedAtItsPositionAndRunsNothing(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, body, want string }{
		{"command env is not a dict", `command(run = ["x"], env = "A=b" + "")`, "env must be a dict of strings"},
		{"command env value is not text", `command(run = ["x"], env = {"A": 1})`, "env must hold strings only"},
		{"command env name is reserved", `command(run = ["x"], env = {"OBEE_STATE_DIR": "b"})`, "env cannot set `OBEE_STATE_DIR`"},
		{"command host with env", `command(run = ["x"], host = "h", env = {"A": "b"})`, "stdin and env cannot be used together with host"},
		{"command stop code is not a number", `command(run = ["x"], stop_on_rc = ["a"])`, "stop_on_rc must hold whole numbers"},
		{"command stop codes are not a list", `command(run = ["x"], stop_on_rc = 3)`, "stop_on_rc must be a list"},
		{"command has an unknown argument", `command(run = ["x"], nope = 1)`, "nope"},
		{"gate has an unknown argument", `gate(nope = 1)`, "nope"},
		{"gate run is not a list", `gate(run = "x" + "")`, "run must be a list of strings"},
		{"agent outputs is not a list", `agent(prompt = "p", model = "m", outputs = "x" + "")`, "outputs must be a list of strings"},
		{"agent inputs is not a dict", `agent(prompt = "p", model = "m", inputs = "x" + "")`, "inputs must be a dict, not a string"},
		{"merge forbid_text is not a list", `merge(head = "h", forbid_text = "x" + "", name = "s")`, "forbid_text must be a list of strings"},
		{"merge own is not a dict", `merge(head = "h", own = "x" + "", name = "s")`, "`own` must be a dict with `logins` and `emails`"},
		{"wait has an unknown argument", `wait(nope = 1)`, "nope"},
		{"env has no name", `env()`, "name"},
		{"env reads a reserved name", `env("OBEE_STATE_DIR")`, "env cannot read `OBEE_STATE_DIR`"},
		{"env reads a name that is not set", `env("NOPE_ZZ")`, "the environment variable NOPE_ZZ is not set"},
		{"agent has no model", `agent(prompt = "p", **{})`, "missing argument for model"},
		{"agent has an empty model", `agent(prompt = "p", model = "")`, "an agent call needs a model"},
		{"env reads a secret", `env("GITHUB_TOKEN")`, "because the name is a secret"},
		{"a dict key is not text in a prompt value", `x = struct(a = {1: 2})` + "\n" + `agent(prompt = "p", model = "m", inputs = {"x": x})`, "dict has int key, want string"},
		{"a function cannot go into a request", `agent(prompt = "p", model = "m", inputs = {"f": env})`, "cannot go into a prompt or a request. Use text, numbers, booleans, lists and dicts (json.encode: in dict key \"f\": cannot encode builtin_function_or_method as JSON)"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			_, err := walkBody(t, h, c.body)

			require.ErrorContains(t, err, c.want)
			assert.Contains(t, err.Error(), "workflow.star:")
			assert.Empty(t, h.commands.ran())
		})
	}
}

func TestCommand_AHostCallWithNoEnvRuns(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkBody(t, h, `command(run = ["x"], host = "h")`)

	require.NoError(t, err)
	assert.Equal(t, []string{"x"}, h.commands.ran())
}

func TestCommand_AProgramThatCannotRunFailsTheItemAtTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{}, errors.New("no such program") }

	_, err := walkBody(t, h, `command(run = ["x"])`)

	require.ErrorContains(t, err, "no such program")
	assert.Contains(t, err.Error(), "workflow.star:2:")
}

func TestGate_AGateThatCannotRunFailsTheItemAtTheCall(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{"project gate": `gate()`, "program": `gate(run = ["x"])`} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.gate.err = errors.New("gate broke")
			h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{}, h.gate.err }

			_, err := walkBody(t, h, body)

			require.ErrorContains(t, err, "could not run")
			require.ErrorContains(t, err, "gate broke")
		})
	}
}

func TestRunStar_AnItemWhoseFieldsCannotBeReadFailsBeforeAnyStep(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.items.err = errors.New("tracker down")

	_, err := walkBody(t, h, `command(run = ["x"])`)

	require.ErrorContains(t, err, "read the fields of item")
	require.ErrorContains(t, err, "tracker down")
	assert.Empty(t, h.commands.ran())
}

func TestRunStar_AFileThatFailsWhileItLoadsNamesThePosition(t *testing.T) {
	t.Parallel()

	_, err := walkText(t, newHarness(t), "fail(\"boom\")\ndef main(item):\n    pass\n")

	require.ErrorContains(t, err, "workflow.star:1:")
	require.ErrorContains(t, err, "boom")
}

func TestRunStar_AnErrorThatIsNotFromTheFileKeepsItsText(t *testing.T) {
	t.Parallel()

	cause := errors.New("plain")

	assert.Equal(t, cause, where(cause))
}

func TestRunStar_ACancelEndsALongRunAsStopped(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		require.NoError(t, client.Cancel(testItem))
		h.tick(t)

		return CommandOutput{}, nil
	}

	_, err := runFileWithin(t, engine, writeFlow(t, bodyFlow("command(run = [\"a\"])\nenv(\"HOME\")\nfor i in range(100000000):\n    pass")))

	require.ErrorContains(t, err, "Workflow")
}

func TestRunStar_ACancelBetweenStepsWithNoLaterBuiltinShowsTheRunAsCancelled(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		require.NoError(t, client.Cancel(testItem))
		h.tick(t)

		return CommandOutput{}, nil
	}

	_, err := runFileWithin(t, engine, writeFlow(t, bodyFlow("command(run = [\"a\"])\nfor i in range(100000000):\n    pass")))

	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "too many steps", "the cancel ends the run, not the step limit")

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, runCancelled, infos[0].State)
}

func TestRunStar_ACancelDuringAWaitShowsTheRunAsCancelled(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground2(engine, writeFlow(t, bodyFlow("wait(question = \"go?\")\ncommand(run = [\"after\"])")))

	waitForTheOwner(t, h)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	select {
	case outcome := <-done:
		// The library returns the cancel in one of two forms, by which side sees it first: the step's own context error, or the cancel row that the result poll reads.
		// Only the form with the context error carries context.Canceled, so both count as the cancel.
		require.Error(t, outcome.err)
		assert.True(t, errors.Is(outcome.err, context.Canceled) || errors.Is(outcome.err, dbos.ErrWorkflowCancelled), "the run ended with %v, not a cancel", outcome.err)
	case <-time.After(eventTimeout):
		t.Fatal("the run did not end after the cancel")
	}

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, runCancelled, infos[0].State)
	assert.Empty(t, h.commands.ran(), "the command after the wait never runs")
}

func TestRunStar_PrintGoesToTheDebugLogAndRunsNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkBody(t, h, `print("hello")`)

	require.NoError(t, err)
	assert.Empty(t, h.commands.ran())
}

func TestScript_AStepOutsideARunReportsItsError(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))
	s := &script{e: engine, ctx: engine.ctx, counts: map[string]int{}}

	_, err := s.env(starlark.NewBuiltin("env", nil), starlark.Tuple{starlark.String("HOME")}, nil)
	require.Error(t, err)

	_, err = s.watchOwner("", true)
	require.ErrorContains(t, err, "cannot show that it waits for the owner to merge")
}

func TestRunStar_AnErrorInANestedFunctionNamesTheCallInThatFunction(t *testing.T) {
	t.Parallel()

	_, err := walkText(t, newHarness(t), "def f():\n    command(run = \"x\" + \"\")\ndef main(item):\n    f()\n")

	require.ErrorContains(t, err, "workflow.star:2:12")
}

func TestPinnedFile_ReadsTheFileOfTheOpenRunOnly(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	client := h.client(t, testVersion)

	file, err := client.PinnedFile(testItem)
	require.NoError(t, err)
	assert.Empty(t, file, "an item with no run has no file")

	path := writeFlow(t, crashFlow)
	interrupted(t, h, path)

	file, err = client.PinnedFile(testItem)
	require.NoError(t, err)
	assert.Equal(t, path, file)

	require.NoError(t, client.Cancel(testItem))

	file, err = client.PinnedFile(testItem)
	require.NoError(t, err)
	assert.Empty(t, file, "a run that ended has no file")
}

func TestStarValues_RoundTripEveryTypeAFileCanHold(t *testing.T) {
	t.Parallel()

	got, err := result(map[string]any{
		"none": nil, "text": "a", "yes": true, "whole": 2, "float": 1.5, "json": float64(4),
		"list": []any{1, "b"}, "dict": map[string]any{"z": 1, "a": []any{}},
	})
	require.NoError(t, err)

	out, err := workflow.ToGo[any](got)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"none": nil, "text": "a", "yes": true, "whole": int64(2), "float": 1.5, "json": int64(4),
		"list": []any{int64(1), "b"}, "dict": map[string]any{"z": int64(1), "a": []any{}},
	}, out)
}

func TestStarValues_ANestedValueThatCannotBeHeldNamesItsOutput(t *testing.T) {
	t.Parallel()

	for name, outputs := range map[string]map[string]any{
		"top":  {"o": func() {}},
		"list": {"o": []any{func() {}}},
		"dict": {"o": map[string]any{"k": func() {}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := result(outputs)

			require.ErrorContains(t, err, `output "o"`)
			require.ErrorContains(t, err, "cannot hold")
		})
	}
}

func TestToGo_RefusesTheValuesAPromptCannotCarry(t *testing.T) {
	t.Parallel()

	fn := starlark.NewBuiltin("f", nil)
	badKey := starlark.NewDict(1)
	require.NoError(t, badKey.SetKey(starlark.MakeInt(1), starlark.None))

	cases := map[string]starlark.Value{
		"a function":      fn,
		"a list of them":  starlark.NewList([]starlark.Value{fn}),
		"a tuple of them": starlark.Tuple{fn},
		"a dict value":    dictOfOne(t, "k", fn),
		"a non-text key":  badKey,
		"a struct field":  starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{"f": fn}),
	}

	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := workflow.ToGo[any](value)

			require.ErrorContains(t, err, "cannot go into a prompt or a request. Use text, numbers, booleans, lists and dicts")
		})
	}

	n, err := workflow.ToGo[any](starlark.Float(2.5))
	require.NoError(t, err)
	assert.InDelta(t, 2.5, n, 0)
}

func TestToGo_ANumberOfAPromptWorksWithEqGtAndPrintf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value starlark.Value
		text  string
		want  string
	}{
		{"int eq", starlark.MakeInt(2), `{{ if eq .n 2 }}yes{{ else }}no{{ end }}`, "yes"},
		{"int gt", starlark.MakeInt(2), `{{ if gt .n 1 }}yes{{ else }}no{{ end }}`, "yes"},
		{"int printf d", starlark.MakeInt(2), `{{ printf "%d" .n }}`, "2"},
		{"int printf v", starlark.MakeInt(2), `{{ printf "%v" .n }}`, "2"},
		{"float eq", starlark.Float(2.5), `{{ if eq .n 2.5 }}yes{{ else }}no{{ end }}`, "yes"},
		{"float gt", starlark.Float(2.5), `{{ if gt .n 2.0 }}yes{{ else }}no{{ end }}`, "yes"},
		{"float printf v", starlark.Float(2.5), `{{ printf "%v" .n }}`, "2.5"},
		{"float printf f", starlark.Float(2.5), `{{ printf "%.1f" .n }}`, "2.5"},
		{"int in a list", starlark.NewList([]starlark.Value{starlark.MakeInt(7)}), `{{ printf "%d" (index .n 0) }}`, "7"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			data, err := workflow.ToGo[map[string]any](dictOfOne(t, "n", c.value))
			require.NoError(t, err)

			got, err := fill(c.text, data)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestToGo_ANumberOfAPromptKeepsAnIntegerBeyondInt64AsDigits(t *testing.T) {
	t.Parallel()

	data, err := workflow.ToGo[map[string]any](dictOfOne(t, "n", starlark.MakeInt64(1).Lsh(70)))
	require.NoError(t, err)

	got, err := fill(`{{ .n }}`, data)
	require.NoError(t, err)
	assert.Equal(t, "1180591620717411303424", got)
}

func TestToGo_ANonFiniteFloatIsRefusedWithTheEncoderError(t *testing.T) {
	t.Parallel()

	_, err := workflow.ToGo[any](starlark.Float(math.NaN()))
	require.EqualError(t, err, "a value cannot go into a prompt or a request. Use text, numbers, booleans, lists and dicts (json.encode: cannot encode non-finite float nan)")

	_, err = workflow.ToGo[any](starlark.NewList([]starlark.Value{starlark.MakeInt(1), starlark.Float(math.NaN())}))
	require.EqualError(t, err, "a value cannot go into a prompt or a request. Use text, numbers, booleans, lists and dicts (json.encode: at list index 1: cannot encode non-finite float nan)")
}

func TestToGo_ARequestedTypeThatTheValueDoesNotFitIsAnError(t *testing.T) {
	t.Parallel()

	_, err := workflow.ToGo[map[string]any](starlark.NewList(nil))

	require.EqualError(t, err, "a list is not the kind of value this place takes")
}

func TestToGo_KeepsANumberBeyondInt64AsItsExactDigits(t *testing.T) {
	t.Parallel()

	n, err := workflow.ToGo[any](starlark.MakeInt64(1).Lsh(70))
	require.NoError(t, err)
	assert.Equal(t, json.Number("1180591620717411303424"), n)

	n, err = workflow.ToGo[any](starlark.MakeInt(1234567))
	require.NoError(t, err)
	assert.Equal(t, "1234567", fmt.Sprint(n), "a prompt prints a whole number in full")
}

func dictOfOne(t *testing.T, key string, value starlark.Value) *starlark.Dict {
	t.Helper()

	dict := starlark.NewDict(1)
	require.NoError(t, dict.SetKey(starlark.String(key), value))

	return dict
}

func TestWords_RefuseWhatIsNotAListOfText(t *testing.T) {
	t.Parallel()

	_, err := workflow.Words("w", starlark.MakeInt(1))
	require.ErrorContains(t, err, "w must be a list of strings")

	_, err = workflow.Words("w", starlark.NewList([]starlark.Value{starlark.MakeInt(1)}))
	require.ErrorContains(t, err, "w must hold strings only")
}
