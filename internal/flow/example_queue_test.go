package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

// These tests run the round of the example: the order of work of the queue pass and the children that it starts.

// startedChildren returns the items that the round started a child for, in sorted order: the children that reached the gate of the real pipeline.
func startedChildren(rig *roundRig) []string {
	return rig.kids.started()
}

func TestExampleQueue_ThePassStartsWorkInFlightBeforeNewWorkAndThenBySeverity(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "21 22 23 10 11")
	rig.setRound("merge", false)

	held := queueItem("10", model.SeverityP0)
	held.Hold = true
	rig.q.items = []model.Item{
		queueItem("21", model.SeverityP2), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP2),
		held, queueItem("11", model.SeverityP0),
	}
	rig.q.pulls = []model.PullRequest{queuePull(t, 8, "23"), queuePull(t, 9, "11", "hold")}

	// The tracker stops showing the items after the first read, so the test reads the start order of pass 1 only. Without the hook the real tracker keeps every item open,
	// and pass 2 starts item 21 in the slot that the first child freed: the test would then read a second pass as well.
	rig.q.afterFirstRead = func(q *fakeQueue) {
		q.mu.Lock()
		defer q.mu.Unlock()

		q.items = nil
	}

	_, err := rig.run(t)
	require.NoError(t, err)

	// The queue has two free slots. 23 has a pull request, so it starts first. 22 is P1 with a higher id than the P2 item 21, so severity alone puts it next.
	// 10 (tracker hold) and 11 (hold label on its pull request) are P0 and would outrank both, so only the ready filter keeps them out.
	assert.Equal(t, []string{"22", "23"}, startedChildren(rig), "the item with an open pull request starts first, then the most severe new item, and a held item never starts")
}

func TestExampleQueue_ARoundWithAnItemListStartsOnlyTheListedItems(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "23")
	rig.setRound("merge", false)
	rig.q.items = []model.Item{queueItem("21", model.SeverityP1), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP1)}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"23"}, startedChildren(rig), "the workflow filters the rows by the list, and the free slots would take two")
}

func TestExampleQueue_ARoundOverAnEmptyQueueStartsNoChild(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, startedChildren(rig))
}

func TestExampleQueue_TheAuditOfEachPassRunsAfterItsQueueReadAndBeforeItsSpawn(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.Positive(t, strings.Index(joined, "queue:round-queue-1#"), joined)
	assert.Less(t, strings.Index(joined, "queue:round-queue-1#"), strings.Index(joined, "agent:queue-audit-1#"), "the audit judges the rows of the queue read of its pass")
	assert.Less(t, strings.Index(joined, "agent:queue-audit-1#"), strings.Index(joined, "spawn:round-spawn-1#"))
}

// auditData is the data file of the first queue audit of a round: the rows and the open pull requests that the agent judges.
type auditData struct {
	Rows []struct {
		Item  string `json:"item"`
		State string `json:"state"`
	} `json:"rows"`
	Pulls []struct {
		Number int    `json:"number"`
		Branch string `json:"branch"`
		Item   string `json:"item"`
	} `json:"pulls"`
}

func TestExampleQueue_TheAuditGetsTheRowsAndThePullRequestsOfTheQueueReadOfItsPass(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.q.pulls = []model.PullRequest{openPullOf(t, 7, "loose", "dev", "owner", "no-issue", true)}

	var (
		mu    sync.Mutex
		texts []string
	)

	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		if strings.Contains(request.Prompt, "## What the queue audit must do") {
			raw, err := os.ReadFile(filepath.Clean(request.DataFile))

			mu.Lock()
			texts = append(texts, string(raw)+errText(err))
			mu.Unlock()
		}

		return inner(request)
	}

	_, err := rig.run(t)
	require.NoError(t, err)

	require.NotEmpty(t, texts, "the round runs a queue audit")

	var got auditData
	require.NoError(t, json.Unmarshal([]byte(texts[0]), &got), texts[0])

	items := make([]string, 0, len(got.Rows))
	for _, row := range got.Rows {
		items = append(items, row.Item)
	}

	assert.ElementsMatch(t, []string{"1", "3"}, items, "the audit reads the rows that the queue read of its pass gave out")
	require.Len(t, got.Pulls, 1, "the audit reads the open pull requests of the same read")
	assert.Equal(t, 7, got.Pulls[0].Number)
	assert.Equal(t, "loose", got.Pulls[0].Branch)
	assert.Empty(t, got.Pulls[0].Item, "a branch that names no open item gives an empty item")
}

// errText is the text of a read error, or "" when the read worked.
func errText(err error) string {
	if err == nil {
		return ""
	}

	return "unreadable: " + err.Error()
}
