package flow

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

func projectVars() map[string]any {
	return map[string]any{"a": json.Number("1"), "b": []any{"x"}, "c": map[string]any{"d": true}, "e": json.Number("1.5")}
}

func TestFacts_TheProjectVarsComeBackAsWritten(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = func(project string) model.Project { return model.Project{Name: project, Vars: projectVars()} }
	h.flowIs(t, "def main(item):\n    f = facts()\n    fail(\"V=%r\" % f.vars)\n")

	outcome := receive(t, runInBackground(h.engine(t), testItem))

	require.ErrorContains(t, outcome.err, `V={"a": 1, "b": ["x"], "c": {"d": True}, "e": 1.5}`)
}

func TestFacts_AProjectWithNoVarsGetsAnEmptyDict(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    f = facts()\n    fail(\"V=%r/%s\" % (f.vars, type(f.vars)))\n")

	outcome := receive(t, runInBackground(h.engine(t), testItem))

	require.ErrorContains(t, outcome.err, "V={}/dict")
}

func TestFacts_TheVarsOfTheFactsAreFrozen(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = func(project string) model.Project { return model.Project{Name: project, Vars: projectVars()} }
	h.flowIs(t, "def main(item):\n    f = facts()\n    f.vars[\"b\"].append(\"y\")\n")

	outcome := receive(t, runInBackground(h.engine(t), testItem))

	require.ErrorContains(t, outcome.err, "frozen")
}

func numberVars() map[string]any {
	return map[string]any{
		"whole":  json.Number("2.0"),
		"big":    json.Number("12345678901234567890123"),
		"bigf":   json.Number("1e+20"),
		"frac":   json.Number("0.1"),
		"one":    json.Number("1"),
		"nest":   []any{[]any{json.Number("1"), []any{}}, map[string]any{"k": []any{nil, true, false}}},
		"empty":  []any{},
		"emptyd": map[string]any{},
		"none":   nil,
		"yes":    true,
		"no":     false,
	}
}

var varsReplayFlow = bodyFlow(`f = facts()
command(run = ["before"], stdin = "%r" % f.vars, name = "before")
ask = wait("go?", name = "ask")
command(run = ["after"], stdin = "%r" % f.vars, name = "after")
`)

func TestFacts_TheVarsKeepTheirNumberTypesOnAReplayFromTheRecordedFacts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	calls := 0
	h.facts = func(project string) model.Project {
		calls++
		if calls > 1 {
			return model.Project{Name: project, Vars: map[string]any{"changed": json.Number("9")}}
		}

		return model.Project{Name: project, Vars: numberVars()}
	}

	file := writeFlow(t, varsReplayFlow)
	interrupted(t, h, file)

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the answer to be accepted", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	outcome := receive(t, done)

	require.NoError(t, outcome.err)
	require.Len(t, h.commands.requests, 2)
	assert.Contains(t, h.commands.requests[0].Stdin, `"whole": 2.0`)
	assert.Contains(t, h.commands.requests[0].Stdin, `"big": 12345678901234567890123`)
	assert.Equal(t, h.commands.requests[0].Stdin, h.commands.requests[1].Stdin)
}

func TestStepResult_ANumberKeepsItsTypeThroughTheRecordedJSON(t *testing.T) {
	t.Parallel()

	recorded, err := json.Marshal(stepResult{Outputs: map[string]any{
		"whole": json.Number("2.0"),
		"big":   json.Number("12345678901234567890123"),
		"bigf":  json.Number("1e+20"),
		"frac":  json.Number("0.1"),
		"one":   int64(1),
		"nest":  []any{json.Number("3.0")},
	}})
	require.NoError(t, err)

	var replayed stepResult

	require.NoError(t, json.Unmarshal(recorded, &replayed))

	got, err := fields(replayed.Outputs)

	require.NoError(t, err)
	assert.Equal(t, "2.0", got["whole"].String())
	assert.Equal(t, "12345678901234567890123", got["big"].String())
	assert.Equal(t, "1e+20", got["bigf"].String())
	assert.Equal(t, "0.1", got["frac"].String())
	assert.Equal(t, "1", got["one"].String())
	assert.Equal(t, "[3.0]", got["nest"].String())
}

func TestStepResult_ARecordedCostReadsTheSameOnAReplay(t *testing.T) {
	t.Parallel()

	recorded, err := json.Marshal(stepResult{Outputs: spendOutputs(agent.Spend{Cost: 1.25, Estimated: true})})
	require.NoError(t, err)

	var replayed stepResult

	require.NoError(t, json.Unmarshal(recorded, &replayed))

	assert.Equal(t, agent.Spend{Cost: 1.25, Estimated: true}, spendOf(replayed.Outputs))
}

func TestSpendOf_ACostThatIsThereButUnreadableMarksTheSpendUnknown(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]any{
		"a number beyond the float range": json.Number("1e400"),
		"a string":                        "1.25",
		"a null":                          nil,
	} {
		got := spendOf(map[string]any{costField: raw, estimatedField: true})

		assert.Equal(t, agent.Spend{Estimated: true, Unknown: true, Unreadable: true}, got, name)
	}
}

func TestSpendOf_ACostThatIsMissingReadsAsNoCostAndNoMark(t *testing.T) {
	t.Parallel()

	assert.Equal(t, agent.Spend{}, spendOf(map[string]any{}))
	assert.Equal(t, agent.Spend{}, spendOf(nil))
	assert.Equal(t, agent.Spend{}, spendOf(spendOutputs(agent.Spend{})))
}

func TestSpendOf_AReadableCostKeepsItsMarksWhateverItsType(t *testing.T) {
	t.Parallel()

	assert.Equal(t, agent.Spend{Cost: 2.5}, spendOf(map[string]any{costField: 2.5}))
	assert.Equal(t, agent.Spend{Cost: 2.5, Unknown: true}, spendOf(map[string]any{costField: json.Number("2.5"), unknownField: true}))
}

func TestStepResult_ARecordedStepThatIsNotValidJSONReturnsTheDecodeError(t *testing.T) {
	t.Parallel()

	var replayed stepResult

	err := replayed.UnmarshalJSON([]byte(`{"outputs": {"cost_usd": `))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode a recorded step:")
	assert.Contains(t, err.Error(), "unexpected EOF")
}

func TestDryRun_TheFactsHoldTheVarsOfTheProjectAndNothingElse(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	file := dryFile(t, "def main(item):\n    f = facts()\n    fail(\"V=%r/R=%r\" % (f.vars, f.repo))\n")
	err := DryRun(t.Context(), file, "12", map[string]string{}, &out, WithVars(projectVars()))

	require.Error(t, err)
	assert.Contains(t, out.String(), `V={"a": 1, "b": ["x"], "c": {"d": True}, "e": 1.5}/R=""`)
}
