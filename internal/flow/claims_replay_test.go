package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

// resumeAfterCrash starts the file again after a crash at its wait, answers the wait and returns the outcome.
func resumeAfterCrash(t *testing.T, h *harness, file string) runOutcome {
	t.Helper()

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	return receive(t, done)
}

func TestClaimStop_ARestartBetweenTheReadAndTheStopReadsTheForgeOnceAndKeepsTheStop(t *testing.T) {
	t.Parallel()

	const flowText = `def main(item):
    pull = pr_read(name = "c")
    wait(name = "ask", question = "go?")
    item_stop(name = "c", step = "claim_stop:c", code = 9, reason = "pull %d labels %s" % (pull.number, ",".join(pull.labels)))
`

	fake := pullFake(func(f *model.PullFacts) { f.Labels = []string{"hold"} })
	h, _ := readsHarness(t, fake)
	file := writeFlow(t, flowText)
	interrupted(t, h, file)
	require.Equal(t, []string{"pull " + testItem}, fake.asked, "the first start reads the forge once")

	fake.mu.Lock()
	fake.facts[testItem] = model.PullFacts{Number: 99, State: "MERGED", Author: "someone"}
	fake.mu.Unlock()

	got := resumeAfterCrash(t, h, file)

	require.NoError(t, got.err)
	assert.Equal(t, []string{"pull " + testItem}, fake.asked, "the replay makes no second read of the forge")
	require.NotNil(t, got.result.Stopped)
	assert.Equal(t, Stopped{Step: "claim_stop:c", RC: 9, Output: "pull 7 labels hold"}, *got.result.Stopped, "the stop of the first read stays")
}

func TestClaimStop_ARestartAfterTheCallReadsNothingAndKeepsTheStop(t *testing.T) {
	t.Parallel()

	const flowText = `load("@beehest//claims.star", "claim_stop")

def main(item):
    c = claim_stop(name = "c", hold = "hold", own = {"logins": ["owner"]})
    wait(name = "ask", question = "go?")
    item_comment(id = "1", key = "k", text = c.stop + "|" + str(c.code))
`

	fake := pullFake(func(f *model.PullFacts) { f.Links = []model.IssueLink{{Number: 3}} })
	fake.issues["#3"] = []string{"maintainer"}
	h, w := readsHarness(t, fake)
	file := writeFlow(t, flowText)
	interrupted(t, h, file)
	require.Equal(t, []string{"pull " + testItem, "issue #3"}, fake.asked, "the first start reads the pull request and the issue once")

	fake.mu.Lock()
	fake.facts[testItem] = model.PullFacts{Number: 7, State: "OPEN", Author: "owner"}
	fake.issues["#3"] = nil
	fake.mu.Unlock()

	got := resumeAfterCrash(t, h, file)

	require.NoError(t, got.err)
	assert.Equal(t, []string{"pull " + testItem, "issue #3"}, fake.asked, "the replay reads nothing again")
	require.NotNil(t, got.result.Stopped)
	assert.Equal(t, "claim_stop:c", got.result.Stopped.Step)
	assert.Equal(t, 11, got.result.Stopped.RC)
	require.Len(t, w.notes, 1)
	assert.Contains(t, w.notes[0], "ASSIGNED|11")
}
