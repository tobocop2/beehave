package flow

import (
	"context"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const quorumFlow = `def main(item):
    r = wait(name = "ask", question = "Ship it?", to = ["ana", "bo", "cy"], quorum = 2, choices = ["yes", "no"])
    if r.timed_out:
        fail("TIMED OUT")
    if sorted(r.answers.keys()) != ["ana", "cy"]:
        fail("WRONG VOICES " + str(r.answers))
    if r.answers["ana"] != "yes" or r.answers["cy"] != "no":
        fail("WRONG TEXT " + str(r.answers))
    if r.answer != "no":
        fail("WRONG ANSWER " + r.answer)
`

// askedOf reads what status shows about the people a wait names.
func askedOf(t *testing.T, client *Client) *Asked {
	t.Helper()

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)

	return infos[0].Asked
}

func TestWait_AQuorumOfNamedPeopleEndsTheWait(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, quorumFlow)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)

	require.NoError(t, client.AnswerAs(testItem, "ana", "yes"))
	eventually(t, func() bool { a := askedOf(t, client); return a != nil && len(a.Heard) == 1 })

	asked := askedOf(t, client)
	assert.Equal(t, []string{"ana", "bo", "cy"}, asked.From)
	assert.Equal(t, 2, asked.Quorum)
	assert.Equal(t, []string{"yes", "no"}, asked.Choices)
	assert.Equal(t, []string{"ana"}, asked.Heard)

	require.NoError(t, client.AnswerAs(testItem, "cy", "no"))

	outcome := <-done

	require.NoError(t, outcome.err)
}

func TestWait_AnAnswerFromSomeoneNotNamedIsRefused(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, quorumFlow)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)
	eventually(t, func() bool { return askedOf(t, client) != nil })

	require.ErrorContains(t, client.AnswerAs(testItem, "dee", "yes"), `"dee" is not one of the people this wait asks: ana, bo, cy`)
	require.ErrorContains(t, client.AnswerAs(testItem, "", "yes"), "name yourself with --as: ana, bo, cy")
	require.ErrorContains(t, client.Answer(testItem, "yes"), "name yourself with --as")

	require.NoError(t, client.AnswerAs(testItem, "ana", "yes"))
	require.NoError(t, client.AnswerAs(testItem, "bo", "no"))
	<-done
}

func TestWait_AnAnswerOutsideTheChoicesIsRefused(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, quorumFlow)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)
	eventually(t, func() bool { return askedOf(t, client) != nil })

	require.ErrorContains(t, client.AnswerAs(testItem, "ana", "maybe"), `"maybe" is not one of the choices: yes, no`)
	assert.Empty(t, askedOf(t, client).Heard)

	require.NoError(t, client.AnswerAs(testItem, "ana", "yes"))
	require.NoError(t, client.AnswerAs(testItem, "bo", "no"))
	<-done
}

func TestWait_ASecondAnswerOfOnePersonReplacesTheFirstAndCountsOnce(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, `def main(item):
    r = wait(name = "ask", question = "Ship it?", to = ["ana", "bo"], quorum = 2)
    if r.answers["ana"] != "second" or r.answers["bo"] != "ok":
        fail("WRONG " + str(r.answers))
`)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)
	eventually(t, func() bool { return askedOf(t, client) != nil })

	require.NoError(t, client.AnswerAs(testItem, "ana", "first"))
	require.NoError(t, client.AnswerAs(testItem, "ana", "second"))
	eventually(t, func() bool { a := askedOf(t, client); return a != nil && len(a.Heard) == 1 })

	select {
	case <-done:
		require.FailNow(t, "one person answered twice and ended a quorum of two")
	default:
	}

	require.NoError(t, client.AnswerAs(testItem, "bo", "ok"))

	require.NoError(t, (<-done).err)
}

func TestWait_AnAnswerWithNoNamesNeedsNoName(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, `def main(item):
    r = wait(name = "ask", question = "Which?", choices = ["a", "b"])
    if r.answer != "b" or r.answers != {}:
        fail("WRONG " + r.answer + str(r.answers))
`)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)
	eventually(t, func() bool { return askedOf(t, client) != nil })

	require.ErrorContains(t, client.Answer(testItem, "c"), `"c" is not one of the choices: a, b`)
	require.ErrorContains(t, client.AnswerAs(testItem, "ana", "b"), "this wait names no people")
	require.NoError(t, client.Answer(testItem, "b"))

	require.NoError(t, (<-done).err)
}

func TestWait_AWaitWithNoNamesRefusesAName(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, "def main(item):\n    wait(name = \"ask\", question = \"Go?\")\n")
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)

	require.ErrorContains(t, client.AnswerAs(testItem, "ana", "yes"), "this wait names no people")
	require.NoError(t, client.Answer(testItem, "yes"))
	require.NoError(t, (<-done).err)
}

func TestWait_ADeadlineEndsAQuorumThatNobodyReached(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, `def main(item):
    r = wait(name = "ask", question = "Ship it?", to = ["ana", "bo"], quorum = 2, timeout = "2s")
    if not r.timed_out:
        fail("NOT TIMED OUT")
    if r.answer != "" or r.answers != {"ana": "yes"}:
        fail("WRONG " + str(r.answers))
`)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)
	eventually(t, func() bool { return askedOf(t, client) != nil })
	require.NoError(t, client.AnswerAs(testItem, "ana", "yes"))

	require.NoError(t, (<-done).err)
}

func TestWait_AReplayCountsTheSameAnswers(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, quorumFlow)
	h.migrated(t)

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	ended := make(chan error, 1)

	go func() {
		_, runErr := first.Run(testItem, testProject)
		ended <- runErr
	}()

	waitForTheOwner(t, h)

	client := h.client(t, testVersion)
	eventually(t, func() bool { return askedOf(t, client) != nil })
	require.NoError(t, client.AnswerAs(testItem, "ana", "yes"))
	eventually(t, func() bool { a := askedOf(t, client); return a != nil && len(a.Heard) == 1 })

	// The engine stops as a crash would, with the first answer recorded and the quorum not reached.
	stop()
	require.NoError(t, first.Shutdown())
	require.Error(t, <-ended)

	next := h.engine(t)
	done := runInBackground(next, testItem)

	eventually(t, func() bool { a := askedOf(t, client); return a != nil && len(a.Heard) == 1 })
	require.NoError(t, client.AnswerAs(testItem, "cy", "no"))

	require.NoError(t, (<-done).err, "the replay counts the answer of ana from its record and the new answer of cy")
}

func TestWait_ARecordFromBeforeTheChangeReadsAnswersAsEmpty(t *testing.T) {
	t.Parallel()

	got, err := result(withZeros(map[string]any{"answer": "go", "timed_out": false}, waitZeros()))
	require.NoError(t, err)

	answers, err := got.Attr("answers")
	require.NoError(t, err)
	assert.Equal(t, "{}", answers.String())
}

const choicesOnlyFlow = `def main(item):
    r = wait(name = "ask", question = "Ship it?", choices = ["yes", "no"])
    if r.answer != "yes":
        fail("WRONG ANSWER " + r.answer)
`

// The ask event alone tells obee answer that a wait is open and what it asks.
func TestWait_TheAskEventHoldsTheQuestionToo(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, choicesOnlyFlow)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitForTheOwner(t, h)
	require.NotNil(t, askedOf(t, client))
	assert.Equal(t, "Ship it?", askedOf(t, client).Question)
	require.NoError(t, client.AnswerAs(testItem, "", "yes"))
	require.NoError(t, (<-done).err)
}

// A wait that asks people opens with one write and ends with one write, both to the ask event. It never writes the question event, so no moment shows
// a question on one event and the kind of wait on another, at the start or at the end.
func TestWait_AWaitWithPeopleOpensAndEndsInOneEvent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, choicesOnlyFlow)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitForTheOwner(t, h)

	question, err := client.questionOf(testRun)
	require.NoError(t, err)
	assert.Empty(t, question, "the question event stays unset while the ask event holds the wait")
	require.NotEmpty(t, askedOf(t, client).Wait, "the ask event names its wait")

	require.NoError(t, client.AnswerAs(testItem, "", "yes"))
	require.NoError(t, (<-done).err)

	asked, err := dbos.GetEvent[Asked](client.flow, testRun, askKey, time.Second)
	require.NoError(t, err)
	assert.Equal(t, Asked{}, asked, "the end clears the one event")

	question, err = client.questionOf(testRun)
	require.NoError(t, err)
	assert.Empty(t, question)
}

const twoWaitsFlow = `def main(item):
    wait(name = "one", question = "First?", to = ["ana"])
    r = wait(name = "two", question = "Second?", to = ["ana"])
    if r.answers != {"ana": "second"}:
        fail("WRONG " + str(r.answers))
`

// A voice carries the id of the wait it answers, and a wait counts only its own. A late vote of wait one, read by wait two of a loop, counts for nothing.
func TestWait_ALateVoiceOfAnEarlierWaitIsNotCounted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, twoWaitsFlow)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitForTheOwner(t, h)

	eventually(t, func() bool { a := askedOf(t, client); return a != nil && a.Question == "First?" })

	first := askedOf(t, client).Wait

	require.NoError(t, client.AnswerAs(testItem, "ana", "first"))
	eventually(t, func() bool { a := askedOf(t, client); return a != nil && a.Question == "Second?" })
	require.NotEqual(t, first, askedOf(t, client).Wait, "each wait has its own id")

	require.NoError(t, dbos.Send(client.flow, testRun, voice{Wait: first, From: "ana", Text: "stale"}, voiceTopic))
	time.Sleep(100 * time.Millisecond)

	assert.Empty(t, askedOf(t, client).Heard, "the vote of the first wait is not a vote of the second")

	require.NoError(t, client.AnswerAs(testItem, "ana", "second"))
	require.NoError(t, (<-done).err)
}
