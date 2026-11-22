package flow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

// startRun runs the file for the test item in the background and returns the channel of its end.
func startRun(t *testing.T, engine *Engine, file string) <-chan runOutcome {
	t.Helper()

	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	return done
}

// stepRecorded waits until the test run has recorded a step of the name.
func stepRecorded(t *testing.T, engine *Engine, name string) {
	t.Helper()

	waitOn(t, waiting[struct{}]{what: "the step " + name, run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)

		return struct{}{}, err == nil && hasStep(steps, name), nil
	}})
}

// text is a message value of one JSON string.
func text(t *testing.T, value string) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(t, err)

	return raw
}

func TestRecv_TakesTheMessageThatBeehaveSendPutsOnTheInboxOnceAndTimesOutOnTheNext(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	done := startRun(t, engine, writeFlow(t, bodyFlow(`a = recv("go", timeout = "1m", name = "one")
b = recv("go", timeout = "50ms", name = "two")
command(name = "got", run = ["got", a.value, a.sender, a.id, str(a.timed_out), str(b.timed_out), str(b.value)])`)))

	stepRecorded(t, engine, "recv:one#1.deadline")

	for range 2 {
		id, err := client.Send(testItem, Outgoing{Topic: "go", Key: "k1", Sender: "cli:ann", Value: text(t, "hello")})
		require.NoError(t, err)
		assert.Equal(t, "k1", id)
	}

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"got hello cli:ann k1 False True None"}, h.commands.ranBy(testItem), "the second send with the key inserted nothing")
}

func TestRecv_KeepsAMessageOfAnotherTopicForALaterRecv(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	done := startRun(t, engine, writeFlow(t, bodyFlow(`a = recv("a", timeout = "1m", name = "a")
b = recv("b", timeout = "1m", name = "b")
command(name = "got", run = ["got", str(a.value), str(b.value)])`)))

	stepRecorded(t, engine, "recv:a#1.deadline")

	_, err := client.Send(testItem, Outgoing{Topic: "b", Value: json.RawMessage(`1`)})
	require.NoError(t, err)
	_, err = client.Send(testItem, Outgoing{Topic: "a", Value: json.RawMessage(`2`)})
	require.NoError(t, err)

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"got 2 1"}, h.commands.ranBy(testItem))

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)
	assert.False(t, hasStep(steps, "recv:b#1.deadline"), "the recv of b took the buffered message and waited for nothing")
}

func TestRecv_TakesTheMessagesOfOneTopicInOrderAndNoTopicThatOnlyStartsWithIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	done := startRun(t, engine, writeFlow(t, bodyFlow(`a = recv("a", timeout = "1m", name = "a")
b1 = recv("b", timeout = "1m", name = "b")
b2 = recv("b", timeout = "1m", name = "b")
g = recv("go", timeout = "50ms", name = "go")
command(name = "got", run = ["got", str(a.value), str(b1.value), str(b2.value), str(g.timed_out)])`)))

	stepRecorded(t, engine, "recv:a#1.deadline")

	for _, msg := range []Outgoing{{Topic: "b", Value: json.RawMessage(`1`)}, {Topic: "gone", Value: json.RawMessage(`9`)}, {Topic: "b", Value: json.RawMessage(`2`)}, {Topic: "a", Value: json.RawMessage(`3`)}} {
		_, err := client.Send(testItem, msg)
		require.NoError(t, err)
	}

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"got 3 1 2 True"}, h.commands.ranBy(testItem), "the first b came first, and the topic gone is not the topic go")
}

func TestRecv_AMessageOfATopicThatOnlyStartsWithTheTopicDoesNotEndAWaitingRecv(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	done := startRun(t, engine, writeFlow(t, bodyFlow(`a = recv("go", timeout = "1m", name = "go")
b = recv("gone", timeout = "200ms", name = "gone")
command(name = "got", run = ["got", str(a.value), str(b.value)])`)))

	stepRecorded(t, engine, "recv:go#1.deadline")

	for _, msg := range []Outgoing{{Topic: "gone", Value: json.RawMessage(`9`)}, {Topic: "go", Value: json.RawMessage(`1`)}} {
		_, err := client.Send(testItem, msg)
		require.NoError(t, err)
	}

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"got 1 9"}, h.commands.ranBy(testItem), "the waiting recv of go kept waiting past gone, and gone waited in the buffer")
}

func TestRecv_AStoppedEngineEndsTheReceive(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	stopCtx, stop := context.WithCancel(t.Context())
	engine, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	done := startRun(t, engine, writeFlow(t, bodyFlow(`recv("go", timeout = "1h", name = "go")`)))
	stepRecorded(t, engine, "recv:go#1.deadline")

	stop()
	require.NoError(t, engine.Shutdown())

	require.ErrorContains(t, receive(t, done).err, "context canceled")
}

func TestRecv_RefusesATopicOrATimeoutThatBreaksTheRule(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ call, want string }{
		"bad topic":      {`recv("Bad " + "Topic", timeout = "1s")`, `topic "Bad Topic" is not allowed`},
		"reserved topic": {`recv("beehave." + "answer", timeout = "1s")`, "reserved"},
		"zero timeout":   {`recv("go", timeout = "0" + "s")`, "timeout"},
		"extra argument": {`recv("go", "1s", "x", "y")`, "recv"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(test.call)))

			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestSend_AChildSendsToItsParentAndTheParentToItsChild(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	flow := `def child(item):
    job = recv("job", timeout = "1m", name = "job")
    send(to = "parent", topic = "found", value = {"from": "child", "job": job.value}, name = "tell")

def main(item):
    k = spawn(items = ["21"], entry = "child", name = "fan")
    sent = send(to = k.runs[0], topic = "job", value = "x", name = "give")
    m = recv("found", timeout = "1m", name = "got")
    gather(k, name = "all")
    command(name = "got", run = ["got", m.value["from"], m.value["job"], m.sender, m.id, sent.id])
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow))

	require.NoError(t, err)
	assert.Equal(t, []string{"got child x item-21-r1 item-21-r1:send:tell#1 item-12-r1:send:give#1"}, h.commands.ranBy(testItem))
}

func TestSend_RefusesATargetThatIsNotTheParentOrAChild(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ to, want string }{
		"no parent":   {`"parent"`, "item 12 has no parent run"},
		"other text":  {`"boss"`, `to is "boss". Give "parent"`},
		"a number":    {`3`, "to is 3. Give"},
		"no run":      {`struct(item = "21")`, "Give \"parent\", or a run that spawn returned"},
		"not a child": {`struct(item = "21", run = "item-21-r1")`, "run item-21-r1 is not a child of this run"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(`send(to = `+test.to+`, topic = "t", value = 1, name = "x")`)))

			require.ErrorContains(t, err, test.want)
			assert.Empty(t, h.commands.ranBy(testItem))
		})
	}
}

func TestSend_RefusesABadTopicAValueThatIsNotJSONAndABadCall(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ call, want string }{
		"bad topic":      {`send(to = "parent", topic = "A" + "b", value = 1)`, `topic "Ab" is not allowed`},
		"not json":       {`send(to = "parent", topic = "t", value = len)`, "a message cannot hold this value"},
		"extra argument": {`send("parent", "t", 1, "x", "y")`, "send"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(test.call)))

			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestSend_ASendToAParentThatIsNotInTheStoreFailsTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	file := writeFlow(t, bodyFlow(`send(to = "parent", topic = "t", value = 1, name = "x")`))
	pinned, err := workflow.Pin(file)
	require.NoError(t, err)

	handle, err := dbos.RunWorkflow(engine.ctx, engine.item, ItemInput{Item: testItem, Project: testProject, File: file, Files: pinned, Parent: "item-99-r1"}, dbos.WithWorkflowID(testRun))
	require.NoError(t, err)

	_, err = handle.GetResult()

	require.ErrorContains(t, err, "cannot send on topic t to run item-99-r1")
}

func TestSend_TheValuePassesTheRedactor(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	secret := "sk-ant-" + strings.Repeat("a", 12)

	flow := `def child(item):
    send(to = "parent", topic = "t", value = "token ` + secret + `", name = "tell")

def main(item):
    k = spawn(items = ["21"], entry = "child", name = "fan")
    m = recv("t", timeout = "1m", name = "got")
    gather(k, name = "all")
    command(name = "got", run = ["got", m.value])
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow))

	require.NoError(t, err)
	require.Len(t, h.commands.ranBy(testItem), 1)
	assert.NotContains(t, h.commands.ranBy(testItem)[0], secret)
}

func TestClientSend_TheValuePassesTheRedactor(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	done := startRun(t, engine, writeFlow(t, bodyFlow(`a = recv("go", timeout = "1m", name = "one")
command(name = "got", run = ["got", a.value])`)))
	stepRecorded(t, engine, "recv:one#1.deadline")

	_, err := client.Send(testItem, Outgoing{Topic: "go", Value: text(t, "token sk-ant-"+strings.Repeat("b", 12))})
	require.NoError(t, err)

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"got token [redacted]"}, h.commands.ranBy(testItem))
}

func TestClientSend_RefusesAnItemWithNoOpenRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	_, err := client.Send(testItem, Outgoing{Topic: "go", Value: text(t, "x")})
	require.ErrorContains(t, err, "item 12 has no open run. Run beehave status")

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(`command(name = "w", run = ["w"])`)))
	require.NoError(t, err)

	_, err = client.Send(testItem, Outgoing{Topic: "go", Value: text(t, "x")})
	require.ErrorContains(t, err, "item 12 has no open run. Run beehave status")

	_, err = client.Send("not an item", Outgoing{Topic: "go", Value: text(t, "x")})
	require.ErrorIs(t, err, ErrBadItem)
}

func TestClientSend_RefusesABadTopicKeyOrValueBeforeItReadsTheStore(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	client := h.client(t, testVersion)

	for name, test := range map[string]struct {
		msg  Outgoing
		want string
	}{
		"bad topic":    {Outgoing{Topic: "Bad Topic", Value: text(t, "x")}, `topic "Bad Topic" is not allowed`},
		"reserved key": {Outgoing{Topic: "go", Key: "beehave.k", Value: text(t, "x")}, `key "beehave.k" is reserved`},
		"not json":     {Outgoing{Topic: "go", Value: json.RawMessage(`{`)}, "not JSON"},
	} {
		_, err := client.Send(testItem, test.msg)

		require.ErrorContains(t, err, test.want, name)
	}
}

func TestClientSend_WithNoKeyMakesAFreshKeyThatTheRuleAccepts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	done := startRun(t, engine, writeFlow(t, bodyFlow(`a = recv("go", timeout = "1m", name = "one")
command(name = "got", run = ["got", a.id])`)))
	stepRecorded(t, engine, "recv:one#1.deadline")

	id, err := client.Send(testItem, Outgoing{Topic: "go", Value: text(t, "x")})
	require.NoError(t, err)
	require.NoError(t, workflow.MessageName("key", id), "the printed id works as --key")

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"got " + id}, h.commands.ranBy(testItem))
}

func TestRecv_AnEndNoticeOfAChildDoesNotEndTheWaitOfARecvAndNoMessageOfAnotherTopicIsLost(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21", "22", "23")
	engine := h.engine(t)
	client := h.client(t, testVersion)

	done := startRun(t, engine, writeFlow(t, `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    a = recv("a", timeout = "1m", name = "a")
    b = recv("b", timeout = "1m", name = "b")
    command(name = "got", run = ["got", str(a.value), str(b.value)])
`))

	stepRecorded(t, engine, "recv:a#1.deadline")
	close(release)
	waitOn(t, conditionOf(runOfEngine(t, engine), func() bool { return itemEnded(engine, "21") && itemEnded(engine, "22") && itemEnded(engine, "23") }))

	_, err := client.Send(testItem, Outgoing{Topic: "b", Value: json.RawMessage(`1`)})
	require.NoError(t, err)
	_, err = client.Send(testItem, Outgoing{Topic: "a", Value: json.RawMessage(`2`)})
	require.NoError(t, err)

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"got 2 1"}, h.commands.ranBy(testItem), "the three end notices did not end the wait of a, and the message of b stayed for the second recv")
}

func TestRecv_AnEndNoticeThatArrivesDuringARecvIsNotKeptForALaterReceive(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).opened(t)
	kept := make(chan []envelope, 1)

	// The workflow waits on topic a the way recv does, and hands the buffer to the test after the wait.
	hold := func(ctx dbos.Context, in ItemInput) (string, error) {
		s := &script{e: engine, ctx: ctx, in: in}

		got, timedOut, err := s.await("a", engine.deps.Clock.reading().Add(time.Minute))
		if err != nil || timedOut {
			return "", errors.Join(err, errors.New("the wait for a ended without a message"))
		}

		kept <- s.inbox

		return string(got.Value), nil
	}

	dbos.RegisterWorkflow(engine.ctx, hold, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(engine.ctx))

	handle, err := dbos.RunWorkflow(engine.ctx, hold, ItemInput{Item: testItem, Project: testProject}, dbos.WithWorkflowID(testRun))
	require.NoError(t, err)

	other := envelope{Topic: "b", Sender: "person", ID: "b-1", Value: json.RawMessage(`1`)}
	require.NoError(t, endNotice(engine.ctx, testRun, "item-21-r1"))
	require.NoError(t, dbos.Send(engine.ctx, testRun, other, inboxTopic))
	require.NoError(t, dbos.Send(engine.ctx, testRun, envelope{Topic: "a", Sender: "person", ID: "a-1", Value: json.RawMessage(`2`)}, inboxTopic))

	got, err := handle.GetResult()
	require.NoError(t, err)
	assert.Equal(t, "2", got)
	assert.Equal(t, []envelope{other}, receive(t, kept), "the buffer keeps the message of b for a later recv and drops the end notice")
}
