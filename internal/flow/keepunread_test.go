package flow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unreadFlow runs an agent step and then takes one message of the topic chat. A message that arrives during the agent step is unread when the step fails.
const unreadFlow = `def main(item):
    agent(name = "work", prompt = "prompts/work.md", model = "m")
    a = recv("chat", timeout = "1s", name = "first")
    command(name = "got", run = ["got", str(a.timed_out), str(a.value)])
`

// takenFlow waits for an answer for 3 s, then takes the message of the topic chat twice. The wait takes a message of another topic and then times out.
const takenFlow = `def main(item):
    agent(name = "work", prompt = "prompts/work.md", model = "m")
    r = wait(name = "ask", question = "What now?", timeout = "3s")
    if r.timed_out:
        fail("TIMED OUT")
    a = recv("chat", timeout = "1s", name = "first")
    b = recv("chat", timeout = "1s", name = "second")
    command(name = "got", run = ["got", str(a.timed_out), str(a.value), str(b.timed_out)])
`

// belowTheCutFlow takes a message of the topic chat before an agent step that fails, and takes the topic again after it. The first recv is below the cut of the resume.
const belowTheCutFlow = `def main(item):
    a = recv("chat", timeout = "10s", name = "first")
    agent(name = "work", prompt = "prompts/work.md", model = "m")
    b = recv("chat", timeout = "1s", name = "second")
    command(name = "got", run = ["got", str(a.value), str(b.timed_out)])
`

func TestResume_AMessageThatATimedOutWaitTookForAnotherTopicReachesALaterRecvOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, takenFlow)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, questionIs(t, h, "What now?"))
	_, err := client.Send(testItem, Outgoing{Topic: "chat", Value: json.RawMessage(`7`)})
	require.NoError(t, err)
	require.ErrorContains(t, receive(t, done).err, "TIMED OUT", "the wait took the message of another topic and then timed out")

	handle := resumeRun(t, engine, client)
	waitOn(t, inASleep(t, engine))
	waitOn(t, ownerAsks(t, h))
	require.NoError(t, client.Reply(testItem, Reply{Text: "go on"}))

	_, err = resultOfHandle(t, handle)
	require.NoError(t, err)
	assert.Equal(t, []string{"got False 7 True"}, h.commands.ranBy(testItem), "the first recv of chat gets the message and the second gets none")
}

func TestResume_AMessageSentDuringAnAgentStepThatThenFailsReachesALaterRecvOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, unreadFlow)
	h.agents.gate = make(chan struct{})
	h.agents.err = errFake
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, condition(func() bool { return h.agents.running() == 1 }))
	_, err := client.Send(testItem, Outgoing{Topic: "chat", Value: json.RawMessage(`7`)})
	require.NoError(t, err)

	close(h.agents.gate)
	require.ErrorContains(t, receive(t, done).err, "fake failure", "the agent step fails with the message unread")

	h.agents.mu.Lock()
	h.agents.err = nil
	h.agents.mu.Unlock()

	_, err = resultOfHandle(t, resumeRun(t, engine, client))
	require.NoError(t, err)
	assert.Equal(t, []string{"got False 7"}, h.commands.ranBy(testItem), "the recv of chat after the resume gets the message sent during the failed step")
}

func TestResume_AMessageTakenBelowTheCutStaysTakenAndALaterRecvDoesNotGetItAgain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, belowTheCutFlow)
	h.agents.err = errFake
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, condition(func() bool {
		_, err := client.Send(testItem, Outgoing{Topic: "chat", Value: json.RawMessage(`7`)})
		return err == nil
	}))
	require.ErrorContains(t, receive(t, done).err, "fake failure", "the agent step fails after the first recv took the message")

	h.agents.mu.Lock()
	h.agents.err = nil
	h.agents.mu.Unlock()

	_, err := resultOfHandle(t, resumeRun(t, engine, client))
	require.NoError(t, err)
	assert.Equal(t, []string{"got 7 True"}, h.commands.ranBy(testItem), "the first recv replays its recorded message, and the second recv finds the message taken, so it times out")
}
