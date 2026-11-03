package flow

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// remindClock is the clock of the wait in these tests. Time moves only when the test moves it.
type remindClock struct{ millis atomic.Int64 }

func newRemindClock(start time.Time) *remindClock {
	c := &remindClock{}
	c.millis.Store(start.UnixMilli())

	return c
}

func (c *remindClock) now() time.Time { return time.UnixMilli(c.millis.Load()) }

func (c *remindClock) advance(d time.Duration) { c.millis.Add(d.Milliseconds()) }

const remindFlow = `def main(item):
    r = wait(name = "ask", question = "Ship it?", to = ["ana", "bo"], timeout = "1h", remind = [0.5, 0.9])
    if r.timed_out:
        fail("TIMED OUT")
    if r.answer != "yes":
        fail("WRONG ANSWER " + r.answer)
`

const remindExpiryFlow = `def main(item):
    r = wait(name = "ask", question = "Ship it?", to = ["ana", "bo"], timeout = "1h", remind = [0.5, 0.9])
    if not r.timed_out:
        fail("NOT TIMED OUT")
`

// remindEngine launches an engine whose wait reads the clock of the test and receives again every few milliseconds.
func remindEngine(t *testing.T, h *harness, clk *remindClock) *Engine {
	t.Helper()

	return launchedWith(t, h, func(deps *Deps) {
		deps.Clock.Reading = clk.now
		deps.Clock.OwnerWait = 10 * time.Millisecond
	})
}

// remindSteps lists the names of the steps of the run that are reminders, in the order the run took them.
func remindSteps(t *testing.T, client *Client) []string {
	t.Helper()

	records, err := client.records(onlyRun(t, client).ID)
	require.NoError(t, err)

	var names []string

	for _, record := range records {
		if len(record.StepName) > len(remindStepPrefix) && record.StepName[:len(remindStepPrefix)] == remindStepPrefix {
			names = append(names, record.StepName)
		}
	}

	return names
}

func TestWaitRemind_StatusShowsTheNextReminderAndMovesItWhenOneIsSent(t *testing.T) {
	t.Parallel()

	start := time.Now()
	clk := newRemindClock(start)
	h := newHarness(t)
	h.flowIs(t, remindFlow)
	engine := remindEngine(t, h, clk)
	client := h.client(t, testVersion)

	done := runInBackground(engine, testItem)

	eventually(t, func() bool { a := askedNow(t, client); return a != nil && !a.NextRemind.IsZero() })
	first := askedNow(t, client)
	assert.WithinDuration(t, start.Add(30*time.Minute), first.NextRemind, time.Second)
	assert.Equal(t, 0, first.Reminded)
	assert.Equal(t, 2, first.Reminders)

	clk.advance(35 * time.Minute)

	eventually(t, func() bool { return remindedIs(t, client, 1) })
	assert.WithinDuration(t, start.Add(54*time.Minute), askedNow(t, client).NextRemind, time.Second)

	require.NoError(t, client.AnswerAs(testItem, "ana", "yes"))
	require.NoError(t, (<-done).err)
}

func TestWaitRemind_ALateWakeSendsEachOwedReminderOnceInOrder(t *testing.T) {
	t.Parallel()

	clk := newRemindClock(time.Now())
	h := newHarness(t)
	h.flowIs(t, remindFlow)
	engine := remindEngine(t, h, clk)
	client := h.client(t, testVersion)

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { a := askedNow(t, client); return a != nil && !a.NextRemind.IsZero() })

	clk.advance(59 * time.Minute)

	eventually(t, func() bool { return remindedIs(t, client, 2) })
	assert.True(t, askedNow(t, client).NextRemind.IsZero(), "no reminder is left")

	require.NoError(t, client.AnswerAs(testItem, "bo", "yes"))
	require.NoError(t, (<-done).err)

	assert.Equal(t, []string{remindStepPrefix + "1", remindStepPrefix + "2"}, remindSteps(t, client))
}

func TestWaitRemind_TheExpiryActsAfterTheOwedReminders(t *testing.T) {
	t.Parallel()

	clk := newRemindClock(time.Now())
	h := newHarness(t)
	h.flowIs(t, remindExpiryFlow)
	engine := remindEngine(t, h, clk)
	client := h.client(t, testVersion)

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { a := askedNow(t, client); return a != nil && !a.NextRemind.IsZero() })

	clk.advance(2 * time.Hour)

	require.NoError(t, (<-done).err, "the wait times out")

	records, err := client.records(onlyRun(t, client).ID)
	require.NoError(t, err)

	var names []string

	for _, record := range records {
		names = append(names, record.StepName)
	}

	first, second, ended := slices.Index(names, remindStepPrefix+"1"), slices.Index(names, remindStepPrefix+"2"), slices.Index(names, "wait:ask#1")
	require.GreaterOrEqual(t, first, 0)
	assert.Less(t, first, second)
	assert.Less(t, second, ended, "both reminders come before the result of the wait")
}

// askedNow is what status shows of the open wait: nil while no run, or no wait, is open.
func askedNow(t *testing.T, client *Client) *Asked {
	t.Helper()

	infos, err := client.Status()
	if err != nil || len(infos) != 1 {
		return nil
	}

	return infos[0].Asked
}

// remindedIs reports whether the open wait shows n reminders sent. A wait that is not open yet shows none.
func remindedIs(t *testing.T, client *Client, n int) bool {
	t.Helper()

	infos, err := client.Status()
	if err != nil || len(infos) != 1 || infos[0].Asked == nil {
		return false
	}

	return infos[0].Asked.Reminded == n
}
