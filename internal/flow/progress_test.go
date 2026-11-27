package flow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const publishingChild = `def worker(item):
    publish("stage", {"n": 1}, name = "one")
    publish("stage", {"n": 2}, name = "two")
    publish("note", "hello", name = "note")
    publish("empty", None, name = "empty")

def main(item):
    runs = spawn(items = ["21"], entry = "worker", name = "fan")
    gather(runs, name = "fan")
`

// runGatherThen runs the parent that gathers the publishing child, then the statements of read.
func runGatherThen(t *testing.T, h *harness, read string) (*Engine, error) {
	t.Helper()

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, publishingChild+"    "+strings.ReplaceAll(strings.TrimSpace(read), "\n", "\n    ")+"\n"), modeOff())

	return engine, err
}

func TestProgress_ReadsTheLastValueThatAChildPublishedUnderAKey(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := runGatherThen(t, h, `p = progress(runs, "stage", name = "peek")
command(name = "log", run = ["log", str(p[0].set), str(p[0].value["n"]), p[0].item, p[0].run])`)

	require.NoError(t, err)
	assert.Equal(t, []string{"log True 2 21 item-21-r1"}, h.commands.ranBy(testItem), "the second publish of the key overwrote the first")
}

func TestProgress_ANoneIsAValueAndAKeyNeverPublishedIsNotSet(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := runGatherThen(t, h, `a = progress(runs, "empty", name = "a")[0]
b = progress(runs, "missing", name = "b")[0]
command(name = "log", run = ["log", str(a.set), str(a.value), str(b.set), str(b.value)])`)

	require.NoError(t, err)
	assert.Equal(t, []string{"log True None False None"}, h.commands.ranBy(testItem))
}

func TestProgress_ReadsEveryChildInOneRecordedStepThatReadsNoEventInTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	engine, err := runGatherThen(t, h, `progress(runs, "stage", name = "peek")`)
	require.NoError(t, err)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	var names []string

	for _, s := range steps {
		names = append(names, s.StepName)
	}

	assert.Equal(t, 1, strings.Count(strings.Join(names, " "), "progress:peek#1"), "one step records the call: %v", names)
	assert.NotContains(t, names, "DBOS.getEvent", "the read goes through the engine and records no event read of its own")
}

func TestProgress_AReplayReturnsTheValuesOfTheFirstRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	engine, err := runGatherThen(t, h, `p = progress(runs, "stage", name = "peek")
command(name = "log", run = ["log", str(p[0].value["n"])])`)
	require.NoError(t, err)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	at := -1

	for _, s := range steps {
		if strings.HasPrefix(s.StepName, "progress") {
			at = s.StepID
		}
	}

	require.GreaterOrEqual(t, at, 0, "the parent recorded a progress step")

	// The store keeps an event as base64 of its JSON: this is {"value":{"n":3}}, the child publishing again after the first read.
	_, err = sqlOf(t, h).ExecContext(t.Context(), "UPDATE workflow_events SET value = 'eyJ2YWx1ZSI6eyJuIjozfX0=' WHERE workflow_uuid = ? AND key = ?", "item-21-r1", "u.stage")
	require.NoError(t, err)

	forked, err := dbos.ForkWorkflow[ItemResult](engine.ctx, dbos.ForkWorkflowInput{OriginalWorkflowID: testRun, StartStep: uint(at + 1)})
	require.NoError(t, err)

	_, err = forked.GetResult()
	require.NoError(t, err)

	assert.Equal(t, []string{"log 2", "log 2"}, h.commands.ranBy(testItem), "the fork replays the recorded read; it does not read the changed event")
}

func TestProgress_RefusesARunThatThisRunDidNotSpawn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	flow := "def main(item):\n    progress([struct(item = \"21\", run = \"item-21-r1\")], \"stage\", name = \"peek\")\n"
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow), modeOff())

	require.ErrorContains(t, err, "run item-21-r1 is not a child of this run")
}

func TestProgress_RefusesRunsThatSpawnDidNotReturn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	flow := "def main(item):\n    progress(\"item-21-r1\", \"stage\", name = \"peek\")\n"
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow), modeOff())

	require.ErrorContains(t, err, "runs must be the value that spawn returned")
}

func TestPublish_RefusesAKeyThatTheRuleRejectsWhenTheKeyIsComputed(t *testing.T) {
	t.Parallel()

	for name, key := range map[string]string{"shape": "Bad Key", "reserved": "beehest.keys"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			flow := "def main(item):\n    k = \"" + key + "\"\n    publish(k, 1, name = \"x\")\n"
			_, err := newHarness(t).engine(t).RunFile(testItem, testProject, writeFlow(t, flow), modeOff())

			require.ErrorContains(t, err, "the key \""+key+"\" is ")
		})
	}
}

func TestProgress_RefusesAKeyThatTheRuleRejectsWhenTheKeyIsComputed(t *testing.T) {
	t.Parallel()

	flow := "def main(item):\n    runs = spawn(items = [\"21\"], entry = \"main\", name = \"fan\")\n    k = \"beehest.keys\"\n    progress(runs, k, name = \"peek\")\n"
	_, err := newHarness(t).engine(t).RunFile(testItem, testProject, writeFlow(t, flow), modeOff())

	require.ErrorContains(t, err, "is reserved")
}

func TestPublish_RefusesAValueThatTheRunCannotKeep(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ value, want string }{
		"over the cap":   {`"x" * 70000`, "at most 65536 bytes"},
		"not JSON-safe":  {`len`, "JSON-safe"},
		"none is stored": {`None`, ""},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			flow := "def main(item):\n    publish(\"k\", " + c.value + ", name = \"x\")\n"
			_, err := newHarness(t).engine(t).RunFile(testItem, testProject, writeFlow(t, flow), modeOff())

			if c.want == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, c.want)
		})
	}
}

func TestPublished_ListsTheKeysOfARunInTheOrderOfTheirFirstPublishWithTheirLastValues(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := runGatherThen(t, h, `pass`)
	require.NoError(t, err)

	got, err := h.client(t, testVersion).Published("item-21-r1")

	require.NoError(t, err)
	assert.Equal(t, []PublishedValue{
		{Key: "stage", Value: []byte(`{"n":2}`)}, {Key: "note", Value: []byte(`"hello"`)}, {Key: "empty", Value: []byte(`null`)},
	}, got)
}

func TestPublished_ARunThatPublishedNothingHasNoKeys(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := runGatherThen(t, h, `pass`)
	require.NoError(t, err)

	got, err := h.client(t, testVersion).Published(testRun)

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestGather_CostUSDIsTheSpendOfTheChildRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", costing(2.5))

	flow := `def worker(item):
    agent(model = "m", prompt = "prompts/a.md", name = "x")
    agent(model = "m", prompt = "prompts/a.md", name = "y")

def main(item):
    done = gather(spawn(items = ["21"], entry = "worker", name = "fan"), name = "fan")
    command(name = "cost", run = ["cost", str(done.results[0].cost_usd)])
`
	_, err := rig.h.engine(t).RunFile(testItem, testProject, capFile(t, flow), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"cost 5.0"}, rig.h.commands.ranBy(testItem), "the two agent steps of the child cost 2.5 each")
}

func TestGatherPriced_ARunThatIsNotInTheStoreCostsNothing(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	got, err := engine.priced("12", []look{{ref: spawnedRun{Item: "21", Run: "item-21-r1"}, out: engine.outcome(spawnedRun{Item: "21", Run: "item-21-r1"}, dbos.WorkflowStatus{}, false)}}, raiseResult{trigger: -1})

	require.NoError(t, err)
	assert.InDelta(t, 0.0, got.Outcomes[0].CostUSD, 0)
}

func TestGatherPriced_ASpendThatCannotBeReadFailsTheStep(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	require.NoError(t, engine.Shutdown())

	_, err := engine.priced("12", []look{{ref: spawnedRun{Item: "21", Run: "item-21-r1"}}}, raiseResult{trigger: -1})

	require.ErrorContains(t, err, "read the spend of run item-21-r1")
}

func TestReadProgress_AStoreThatCannotBeReadFailsTheStep(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	require.NoError(t, engine.Shutdown())

	_, err := engine.readProgress([]spawnedRun{{Item: "21", Run: "item-21-r1"}}, "stage")

	require.ErrorContains(t, err, "read the key stage of run item-21-r1")
}

func TestPublish_ACallOutsideAStepCannotKeepTheKey(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	_, err := engine.setEvents(context.Background(), nil, "stage", "1")

	require.ErrorContains(t, err, "publish the key stage: the step has no run to hold the key")
}

func TestPublish_ARunContextThatIsNoStepCannotWriteTheEvent(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	_, err := engine.setEvents(engine.ctx, nil, "stage", "1")

	require.ErrorContains(t, err, "publish the key stage: ")
}

func TestPublishAndProgress_RefuseArgumentsThatAreMissing(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{"publish": `publish(**{"key": "a"})`, "progress": `progress(**{"key": "a"})`} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := newHarness(t).engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    "+call+"\n"), modeOff())

			require.ErrorContains(t, err, name)
		})
	}
}

func TestProgressValue_AValueThatIsNotJSONFailsTheCall(t *testing.T) {
	t.Parallel()

	_, err := progressValue([]childProgress{{Item: "21", Run: "item-21-r1", Set: true, Value: json.RawMessage("not json")}})

	require.ErrorContains(t, err, "read the value of run item-21-r1")
}

func TestProgress_AnEventThatCannotBeDecodedFailsTheStep(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Run[0] != "true" {
			return nil
		}

		_, err := sqlOf(t, h).ExecContext(ctx, "UPDATE workflow_events SET value = 'bm90anNvbg==' WHERE workflow_uuid = ? AND key = ?", "item-21-r1", "u.stage")

		return err
	}

	_, err := runGatherThen(t, h, "command(name = \"mid\", run = [\"true\"])\nprogress(runs, \"stage\", name = \"peek\")")

	require.ErrorContains(t, err, "read the key stage of run item-21-r1")
}

func TestPublished_AClosedClientCannotReadTheKeys(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	client, err := OpenClient(t.Context(), h.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, err = client.Published(testRun)

	require.ErrorContains(t, err, "read the keys that "+testRun+" published")
}

func TestPublished_AKeyThatTheListNamesAndNoEventHoldsIsAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    pass\n"), modeOff())
	require.NoError(t, err)

	// WyJnaG9zdCJd is the base64 of ["ghost"], the way the store keeps the list of keys.
	_, err = sqlOf(t, h).ExecContext(t.Context(), "INSERT INTO workflow_events (workflow_uuid, key, value, serialization) VALUES (?, ?, ?, ?)", testRun, "beehest.keys", "WyJnaG9zdCJd", "DBOS_JSON")
	require.NoError(t, err)

	_, err = h.client(t, testVersion).Published(testRun)

	require.ErrorContains(t, err, "read the key ghost of "+testRun)
}

func TestPublish_AValueThatHoldsAnAnthropicKeyIsRedactedForProgressAndForStatusEvents(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	flow := `def worker(item):
    publish("note", {"text": "key sk-ant-abcdefghijklmnop0123 here"}, name = "note")

def main(item):
    runs = spawn(items = ["21"], entry = "worker", name = "fan")
    gather(runs, name = "fan")
    p = progress(runs, "note", name = "peek")
    command(name = "log", run = ["log", p[0].value["text"]])
`
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, flow), modeOff())
	require.NoError(t, err)

	ran := h.commands.ranBy(testItem)
	require.Len(t, ran, 1)
	assert.Contains(t, ran[0], "[redacted]", "progress returns the redacted value")
	assert.NotContains(t, ran[0], "sk-ant")

	got, err := h.client(t, testVersion).Published("item-21-r1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Contains(t, string(got[0].Value), "[redacted]", "status --events reads the redacted value")
	assert.NotContains(t, string(got[0].Value), "sk-ant")
}

func TestGather_CostUSDAtDepthThreeIncludesEveryRunBelowTheChild(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", costing(2.5))

	flow := `def leaf(item):
    agent(model = "m", prompt = "prompts/a.md", name = "leaf")

def middle(item):
    agent(model = "m", prompt = "prompts/a.md", name = "middle")
    gather(spawn(items = ["23"], entry = "leaf", name = "deep"), name = "deep")

def child(item):
    agent(model = "m", prompt = "prompts/a.md", name = "child")
    gather(spawn(items = ["22"], entry = "middle", name = "mid"), name = "mid")

def main(item):
    done = gather(spawn(items = ["21"], entry = "child", name = "fan"), name = "fan")
    command(name = "cost", run = ["cost", str(done.results[0].cost_usd)])
`
	_, err := rig.h.engine(t).RunFile(testItem, testProject, capFile(t, flow), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"cost 7.5"}, rig.h.commands.ranBy(testItem), "the child, its child and the child of that one cost 2.5 each")
}
