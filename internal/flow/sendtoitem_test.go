package flow

import (
	"bytes"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// crashAtSend makes an engine whose first redaction of the value "pr 5" cancels the run of the test item. The send redacts its value after the step
// that records its target and before the step that sends, which is where a crash of the engine leaves a run that replays the target.
func crashAtSend(t *testing.T, h *harness) *Engine {
	t.Helper()

	crash := newCrash(t, h)

	return h.engineWith(t, func(e *Engine) {
		inner := e.redact
		e.redact = func(text string) string {
			if text == `"pr 5"` {
				crash.after()
			}

			return inner(text)
		}
	})
}

func TestSendToItem_AReplayAfterTheTargetStepSendsOnceToTheRecordedRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := crashAtSend(t, h)
	waiting := waitingItem(t, engine, "21", testProject)

	_, err := engine.RunFile(testItem, testProject, sendFlow(t, "item:21"))
	require.Error(t, err, "the crash ends the sender")

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)
	require.True(t, hasStep(steps, "send:tell#1.to"), "the crash came after the target step")
	require.False(t, hasStep(steps, "send:tell#1"), "the crash came before the send step")

	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))
	require.NoError(t, err)
	require.NoError(t, receive(t, waiting).err)

	assert.Equal(t, []string{"got pr 5 item-12-r1 item-12-r1:send:tell#1 True"}, h.commands.ranBy("21"), "one send, and one receive: the second recv of the receiver times out")
}

func TestSendToItem_AReplayDoesNotLookTheItemUpAgain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := crashAtSend(t, h)
	waiting := waitingItem(t, engine, "21", testProject)

	_, err := engine.RunFile(testItem, testProject, sendFlow(t, "item:21"))
	require.Error(t, err, "the crash ends the sender")

	require.NoError(t, h.client(t, testVersion).Cancel("21"), "the target ends between the target step and the send")
	h.tick(t)
	require.Error(t, receive(t, waiting).err)

	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))
	require.NoError(t, err, "a lookup on the replay would find no open run, and the recorded run needs none")
}

// builtSendFlow is a workflow file whose main sends to "item:" and an id that the file builds at run time, so the check of the file cannot read it.
func builtSendFlow(t *testing.T, id string) string {
	t.Helper()

	return writeFlow(t, bodyFlow(`send(to = "item:" + ["`+id+`"][0], topic = "landed", value = "pr 5", name = "tell")`))
}

func TestSendToItem_AnIdOfALiteralTargetThatIsNotAnItemIdIsRefusedBeforeTheRunStarts(t *testing.T) {
	t.Parallel()

	_, err := newHarness(t).engine(t).RunFile(testItem, testProject, sendFlow(t, "item:not an item"))

	require.ErrorContains(t, err, `to is "item:not an item"`)
	require.NotErrorIs(t, err, ErrBadItem)
}

func TestSendToItem_ADryRunRefusesAnIdThatIsNotAnItemId(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), builtSendFlow(t, "not an item"), "12", map[string]string{}, &out)

	require.ErrorIs(t, err, ErrBadItem, out.String())
}

func TestSendToItem_AnItemMaySendToItsOwnOpenRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(`send(to = "item:12", topic = "landed", value = "me", name = "self")
m = recv("landed", timeout = "1m", name = "got")
command(name = "got", run = ["got", m.value])`)))

	require.NoError(t, err)
	assert.Equal(t, []string{"got me"}, h.commands.ranBy(testItem))
}

func TestSendToItem_ARunWhoseInputCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	enqueueRun(t, h.client(t, testVersion), runID("21", 1), "not an item input", engine.version)

	_, err := engine.RunFile(testItem, testProject, sendFlow(t, "item:21"))

	require.ErrorContains(t, err, "read the input of run item-21-r1")
}

func TestSendToItem_ARunOfAnotherBuildIsStranded(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	startOpenRun(t, h, engine, "21", testProject)

	_, err := sqlOf(t, h).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = 'v0+core1' WHERE workflow_uuid = 'item-21-r1'")
	require.NoError(t, err)

	_, err = engine.RunFile(testItem, testProject, sendFlow(t, "item:21"))

	require.ErrorIs(t, err, errStranded)
}
