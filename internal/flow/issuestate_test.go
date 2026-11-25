package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

// issueStateFlow is a workflow file whose main passes the state that the call `call` read to the command seen.
func issueStateFlow(call string) string {
	return "def main(item):\n    command(name = \"seen\", run = [\"seen\", " + call + ".state])\n"
}

func TestIssueState_ReturnsOpenOrClosed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	q := &fakeQueue{issueStates: map[int]string{7: "OPEN", 8: "CLOSED"}}
	h.queue = q
	engine := h.engine(t)

	body := "def main(item):\n    command(name = \"seen\", run = [\"seen\", issue_state(number = 7, name = \"a\").state, issue_state(number = 8, name = \"b\").state])\n"
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, body), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"OPEN", "CLOSED"}, seenBy(t, h))
	assert.Equal(t, []int{7, 8}, q.issueAsked, "each call reads the number it names, once")
	assert.Subset(t, stepNames(t, engine), []string{"issue_state:a#1", "issue_state:b#2"})
}

func TestIssueState_FoundIsTrueForARead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.queue = &fakeQueue{issueStates: map[int]string{7: "OPEN"}}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    command(name = \"seen\", run = [\"seen\", str(issue_state(number = 7).found)])\n"), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"True"}, seenBy(t, h))
}

func TestIssueState_AProjectWhoseTrackerHasNoIssueNumbersGivesFoundFalseAndNoState(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.queue = &fakeQueue{noIssueNumbers: true, issueStates: map[int]string{7: "OPEN"}}

	body := "def main(item):\n    r = issue_state(number = 7)\n    command(name = \"seen\", run = [\"seen\", str(r.found), \"[\" + r.state + \"]\"])\n"
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, body), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"False", "[]"}, seenBy(t, h), "no read ran, so the result holds no state and the run goes on")
}

func TestIssueState_ANumberBelowOneIsRefusedBeforeAnyRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	q := &fakeQueue{}
	h.queue = q

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, issueStateFlow(`issue_state(number = 0)`)), modeOff())

	require.ErrorContains(t, err, "`number` is 0")
	assert.Empty(t, q.issueAsked)
}

func TestIssueState_ABuildWithNoQueueReaderFailsTheCall(t *testing.T) {
	t.Parallel()

	_, err := newHarness(t).engine(t).RunFile(testItem, testProject, writeFlow(t, issueStateFlow(`issue_state(number = 7)`)), modeOff())

	require.ErrorIs(t, err, errNoIssueStateReader)
}

// issueReadFlow is a workflow file whose main passes the found, state and error fields of one read of issue 7 to the command seen.
const issueReadFlow = "def main(item):\n    r = issue_state(number = 7)\n    command(name = \"seen\", run = [\"seen\", str(r.found), \"[\" + r.state + \"]\", r.error])\n"

func TestIssueState_AReadThatFailsGivesTheErrorAndNoState(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.queue = &fakeQueue{issueStates: map[int]string{7: issueOpen}, issueErr: errFake}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, issueReadFlow), modeOff())

	require.NoError(t, err, "the caller reads the error and decides; the check after a batch makes it a finding")
	assert.Equal(t, []string{"seen False [] " + errFake.Error()}, h.commands.ranBy(testItem), "a failed read gives no state, so no caller reads a guess")
}

func TestIssueState_AnAnswerThatIsNeitherStateGivesTheErrorAndNoState(t *testing.T) {
	t.Parallel()

	for name, state := range map[string]string{"empty": "", "other": "MERGED"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.queue = &fakeQueue{issueStates: map[int]string{7: state}}

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, issueReadFlow), modeOff())

			require.NoError(t, err)
			assert.Equal(t, []string{`seen False [] the forge answered "` + state + `", not OPEN or CLOSED`}, h.commands.ranBy(testItem))
		})
	}
}

func TestIssueState_AReadThatTheEndOfTheRunStopsEndsTheStep(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errFake)

	s := &script{e: &Engine{deps: Deps{Queue: &fakeQueue{issueErr: context.Canceled}}}}

	_, err := s.readIssueState(ctx, 7)

	require.ErrorIs(t, err, errFake, "a stopped read is no answer of the forge, so it is not recorded as one")
}

func TestIssueState_AFoundStateHasNoError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.queue = &fakeQueue{issueStates: map[int]string{7: issueClosed}}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, issueReadFlow), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"seen True [CLOSED] "}, h.commands.ranBy(testItem))
}

func TestIssueState_ACallWithAnArgumentOfTheWrongTypeIsRefusedByTheBuiltin(t *testing.T) {
	t.Parallel()

	s := &script{e: &Engine{deps: Deps{Queue: &fakeQueue{}}}}

	_, err := s.issueState(starlark.NewBuiltin(issueStateKind, nil), nil, []starlark.Tuple{{starlark.String("number"), starlark.String("7")}})

	require.ErrorContains(t, err, "issue_state")
}

func TestIssueState_AReplayReturnsTheFirstReadNotAFreshOne(t *testing.T) {
	t.Parallel()

	file := writeFlow(t, "def main(item):\n    s = issue_state(number = 7, name = \"s\")\n    wait(name = \"ask\", question = \"go?\")\n    command(name = \"seen\", run = [\"seen\", s.state])\n")
	h := newHarness(t)
	q := &fakeQueue{issueStates: map[int]string{7: "OPEN"}}
	h.queue = q
	interrupted(t, h, file)

	q.mu.Lock()
	q.issueStates[7] = "CLOSED"
	q.mu.Unlock()

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground2(next, file)

	waitOn(t, ownerAsks(t, h))
	require.NoError(t, client.Reply(testItem, Reply{Text: "yes"}))
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, []string{"OPEN"}, seenBy(t, h), "the state of the first read comes back, whatever the forge says now")
	assert.Equal(t, []int{7}, q.issueAsked, "the replay reads the forge no second time")
}
