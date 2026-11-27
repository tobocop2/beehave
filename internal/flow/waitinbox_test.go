package flow

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// questionIs waits until the open run shows this question.
func questionIs(t *testing.T, h *harness, question string) waiting[struct{}] {
	t.Helper()

	read := newStatusRead(h.client(t, testVersion))

	return waiting[struct{}]{what: "the open run to show the question " + question, run: runOfClient(t, h.client(t, testVersion)), last: read.last, read: func() (struct{}, bool, error) {
		infos := read.next()

		return struct{}{}, read.err == nil && len(infos) == 1 && infos[0].Question == question, nil
	}}
}

// sendWhenRunExists puts an envelope on the inbox of the test run as soon as the run exists.
func sendWhenRunExists(t *testing.T, h *harness, env envelope) {
	t.Helper()

	client := h.client(t, testVersion)

	require.Eventually(t, func() bool { return sendEnvelope(client.flow, testRun, env) == nil }, 10*time.Second, 20*time.Millisecond, "the run exists to take a message")
}

const twoPlainWaitsFlow = `def main(item):
    wait(name = "one", question = "First?")
    w = wait(name = "two", question = "Second?", timeout = "3s")
    if not w.timed_out:
        fail("SECOND GOT " + w.answer)
`

func TestReply_TwoAnswersWithOneKeyAreOneMessage(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, twoPlainWaitsFlow)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, questionIs(t, h, "First?"))
	require.NoError(t, client.Reply(testItem, Reply{Text: "go", Key: "k1"}))
	waitOn(t, questionIs(t, h, "Second?"))
	require.NoError(t, client.Reply(testItem, Reply{Text: "go", Key: "k1"}))

	require.NoError(t, receive(t, done).err, "the second wait got no answer, so it timed out")
}

func TestReply_TwoAnswersWithNoKeyAreTwoMessages(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    wait(name = \"one\", question = \"First?\")\n    w = wait(name = \"two\", question = \"Second?\")\n    if w.answer != \"again\":\n        fail(\"SECOND GOT \" + w.answer)\n")
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, questionIs(t, h, "First?"))
	require.NoError(t, client.Reply(testItem, Reply{Text: "again"}))
	waitOn(t, questionIs(t, h, "Second?"))
	require.NoError(t, client.Reply(testItem, Reply{Text: "again"}))

	require.NoError(t, receive(t, done).err)
}

func TestReply_ABadKeyIsRefusedBeforeTheRunIsRead(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)

	err := client.Reply(testItem, Reply{Text: "go", Key: "Bad Key"})

	require.ErrorContains(t, err, "key")
	assert.NotErrorIs(t, err, errNoRun, "the key is checked before the run is looked up")
}

func TestWait_APlainWaitDropsTheAnswerOfAWaitThatNamesPeople(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    w = wait(name = \"ask\", question = \"Go?\", timeout = \"2s\")\n    if not w.timed_out:\n        fail(\"TOOK \" + w.answer)\n")
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, questionIs(t, h, "Go?"))
	require.NoError(t, sendAnswerEnvelope(client.flow, testRun, voice{Wait: "w1", From: "ana", Text: "stale"}))

	require.NoError(t, receive(t, done).err, "the plain wait kept waiting and timed out")
}

func TestWait_AnAnswerThatAnEarlierRecvReadIsTakenByTheNextWait(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    recv(\"go\", timeout = \"2s\", name = \"r\")\n    w = wait(name = \"ask\", question = \"Go?\", timeout = \"20s\")\n    if w.answer != \"early\":\n        fail(\"GOT \" + str(w.answer))\n")
	engine := h.engine(t)
	done := runInBackground(engine, testItem)

	sendWhenRunExists(t, h, envelope{Topic: answerTopic, ID: "early", Value: json.RawMessage(`{"text":"early","from":""}`)})

	require.NoError(t, receive(t, done).err, "the answer that recv put aside reached the wait")
}

func TestWait_AnAnswerWithNoTextFailsTheWait(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    wait(name = \"ask\", question = \"Go?\", timeout = \"20s\")\n")
	engine := h.engine(t)
	done := runInBackground(engine, testItem)

	sendWhenRunExists(t, h, envelope{Topic: answerTopic, ID: "bad", Sender: "cli", Value: json.RawMessage(`7`)})

	require.ErrorContains(t, receive(t, done).err, "got an answer from cli that holds no text")
}

func TestWait_AReceiveOutsideARunFails(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	_, _, err := waiter(engine).receiveAnswer(0)
	require.Error(t, err, "the receive needs a run")

	_, _, err = waiter(engine).receiveAnswer(time.Hour)
	require.Error(t, err, "the end of the deadline is a recorded step of a run")
}

func TestWait_AnAnswerThatHoldsASecretArrivesRedacted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    w = wait(name = \"ask\", question = \"Token?\")\n    if \"sk-ant-\" in w.answer or w.answer != \"use [redacted] now\":\n        fail(\"GOT \" + w.answer)\n")
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, questionIs(t, h, "Token?"))
	require.NoError(t, client.Reply(testItem, Reply{Text: "use sk-ant-abcdefgh12345678 now"}))

	require.NoError(t, receive(t, done).err, "the wait holds the redacted text")
}

func TestWait_AnAnswerFromANamedPersonThatHoldsASecretArrivesRedacted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    r = wait(name = \"ask\", question = \"Token?\", to = [\"ana\"])\n    if r.answers[\"ana\"] != \"use [redacted] now\":\n        fail(\"GOT \" + str(r.answers))\n")
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, questionIs(t, h, "Token?"))
	require.NoError(t, client.Reply(testItem, Reply{From: "ana", Text: "use sk-ant-abcdefgh12345678 now"}))

	require.NoError(t, receive(t, done).err, "the voice of the named person holds the redacted text")
}

func TestWait_AMessageOfAnotherTopicDoesNotExtendTheDeadline(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, fmt.Sprintf(timedWaitFlow, "1s"))
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, questionIs(t, h, "What now?"))

	started := time.Now()
	_, err := client.Send(testItem, Outgoing{Topic: "chat", Value: json.RawMessage(`"hello"`)})
	require.NoError(t, err)

	require.ErrorContains(t, receive(t, done).err, "TIMED OUT")
	assert.Less(t, time.Since(started), 2500*time.Millisecond, "the wait ends at its own deadline, not one after the message")
}

func TestReply_AKeyThatBeehestSendUsedStillSendsTheAnswer(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, fmt.Sprintf(timedWaitFlow, "1h"))
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, questionIs(t, h, "What now?"))

	_, err := client.Send(testItem, Outgoing{Topic: "chat", Key: "same", Value: json.RawMessage(`"hello"`)})
	require.NoError(t, err)
	require.NoError(t, client.Reply(testItem, Reply{Text: "go on", Key: "same"}))

	require.NoError(t, receive(t, done).err, "the answer was not dropped as a repeat of the message")
}
