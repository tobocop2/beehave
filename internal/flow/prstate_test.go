package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prStateFlow is a workflow file whose main passes the state that the call `call` reads to the command seen.
func prStateFlow(call string) string {
	return "def main(item):\n    command(name = \"seen\", run = [\"seen\", " + call + ".state])\n"
}

func TestPRState_ReadsTheStateOfTheNumberItNamesAndRecordsTheStep(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	q := &fakeQueue{states: map[int]string{41: "MERGED"}}
	h.queue = q
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, prStateFlow(`pr_state(pr = 41, name = "s")`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"MERGED"}, seenBy(t, h))
	assert.Equal(t, []int{41}, q.stateAsked, "the call reads the number it names, once")
	assert.Contains(t, stepNames(t, engine), "pr_state:s#1")
}

func TestPRState_ANumberBelowOneIsRefusedBeforeAnyRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	q := &fakeQueue{}
	h.queue = q

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, prStateFlow(`pr_state(pr = 0)`)), modeOff())

	require.ErrorContains(t, err, "`pr` is 0")
	assert.Empty(t, q.stateAsked)
}

func TestPRState_NumberOneIsTheLowestNumberItReads(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	q := &fakeQueue{states: map[int]string{1: "OPEN"}}
	h.queue = q

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, prStateFlow(`pr_state(pr = 1)`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"OPEN"}, seenBy(t, h))
	assert.Equal(t, []int{1}, q.stateAsked)
}

func TestPRState_ANumberThatIsNotAnIntegerIsRefusedBeforeAnyRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	q := &fakeQueue{}
	h.queue = q

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    number = \"41\"\n    pr_state(pr = number)\n"), modeOff())

	require.ErrorContains(t, err, `pr_state: for parameter "pr": got string, want int`)
	assert.Empty(t, q.stateAsked)
}

func TestPRState_ABuildWithNoQueueReaderFailsTheCall(t *testing.T) {
	t.Parallel()

	_, err := newHarness(t).engine(t).RunFile(testItem, testProject, writeFlow(t, prStateFlow(`pr_state(pr = 41)`)), modeOff())

	require.ErrorIs(t, err, errNoStateReader)
}

func TestPRState_AReadThatFailsEndsTheStepWithAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.queue = &fakeQueue{stateErr: errFake}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, prStateFlow(`pr_state(pr = 41)`)), modeOff())

	require.ErrorIs(t, err, errFake)
	assert.ErrorContains(t, err, "read the state of pull request 41")
}
