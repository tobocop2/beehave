package flow

import (
	"bytes"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitingItem starts the item in the background with a flow that receives one message on the topic landed, and returns when its recv has recorded its deadline.
func waitingItem(t *testing.T, engine *Engine, item, project string) <-chan runOutcome {
	t.Helper()

	file := writeFlow(t, bodyFlow(`got = recv("landed", timeout = "1m", name = "wait")
again = recv("landed", timeout = "50ms", name = "again")
command(name = "got", run = ["got", got.value, got.sender, got.id, str(again.timed_out)])`))
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(item, project, file)
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, condition(func() bool {
		steps, err := dbos.GetWorkflowSteps(engine.ctx, runID(item, 1))

		return err == nil && hasStep(steps, "recv:wait#1.deadline")
	}))

	return done
}

// sendFlow is a workflow file whose main sends one message to the target.
func sendFlow(t *testing.T, target string) string {
	t.Helper()

	return writeFlow(t, bodyFlow(`send(to = "`+target+`", topic = "landed", value = "pr 5", name = "tell")`))
}

func TestSendToItem_ADependencyItemTellsTheItemThatWaitsOnItAndTheMessageArrivesOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	waiting := waitingItem(t, engine, "21", testProject)

	_, err := engine.RunFile(testItem, testProject, sendFlow(t, "item:21"))
	require.NoError(t, err)
	require.NoError(t, receive(t, waiting).err)

	assert.Equal(t, []string{"got pr 5 item-12-r1 item-12-r1:send:tell#1 True"}, h.commands.ranBy("21"), "one delivery, from the run of item 12")

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)
	assert.True(t, hasStep(steps, "send:tell#1.to"), "the run that the send names is a recorded step, so a replay sends to the same run")
}

func TestSendToItem_AnItemWithNoOpenRunOrAFinishedRunIsAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, sendFlow(t, "item:30"))
	require.ErrorContains(t, err, "item 30 has no open run")

	_, err = engine.RunFile("21", testProject, writeFlow(t, bodyFlow(`command(name = "w", run = ["w"])`)))
	require.NoError(t, err)

	// Item 12 failed its first run at item 30, and RunFile of a failed item reports that run. A new sender gets its own first run.
	_, err = engine.RunFile("13", testProject, sendFlow(t, "item:21"))
	require.ErrorContains(t, err, "item 21 has no open run")
}

func TestSendToItem_AnItemThatIsNotAnItemIdIsAnError(t *testing.T) {
	t.Parallel()

	_, err := newHarness(t).engine(t).RunFile(testItem, testProject, builtSendFlow(t, "not an item"))

	require.ErrorIs(t, err, ErrBadItem)
}

func TestSendToItem_AnItemWithAnOpenRunUnderAnotherProjectIsAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	startOpenRun(t, h, engine, "21", "other")

	_, err := engine.RunFile(testItem, testProject, sendFlow(t, "item:21"))

	require.ErrorContains(t, err, "item 21 has an open run under project other")
	require.ErrorContains(t, err, "project p")
}

func TestSendToItem_ADryRunEndsCleanAtASendToAnItemBecauseNoOtherRunExists(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), sendFlow(t, "item:30"), "12", map[string]string{}, &out)

	require.ErrorIs(t, err, ErrDryRunEndedEarly, out.String())
	assert.Contains(t, out.String(), "send a message to an item")
}

func TestSendToItem_KeepsTheSendToTheParentAndToAChild(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	flow := `def child(item):
    job = recv("job", timeout = "1m", name = "job")
    send(to = "parent", topic = "found", value = job.value, name = "tell")

def main(item):
    k = spawn(items = ["21"], entry = "child", name = "fan")
    send(to = k.runs[0], topic = "job", value = "x", name = "give")
    m = recv("found", timeout = "1m", name = "got")
    gather(k, name = "all")
    command(name = "got", run = ["got", m.value])
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow))

	require.NoError(t, err)
	assert.Equal(t, []string{"got x"}, h.commands.ranBy(testItem))
}
