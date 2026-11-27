package flow

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// These tests run examples/remediation/workflow.star unchanged, with the real pipeline and fake agents, as every test of the round rig does.

func TestExampleDone_ARoundWithNoIntegrationPullRequestMergesOneItemInTheFirstPassAndClosesItsIssueWithNoGhCall(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.setRound("integration_pr", json.Number("0"))

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the first pass lands the approved head of the item")
	assert.Equal(t, []string{testProject + "|1|closed"}, closedOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "merged"), 1)

	// The comment comes before the status, and both come from the tracker builtins.
	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.Less(t, strings.Index(joined, "item_comment:merged-1#"), strings.Index(joined, "item_status:closed-1#"), "the comment is on the issue before the issue closes")
	assert.Empty(t, commandsNamed(rig, "gh"), "no command of the run calls gh")

	for _, request := range rig.h.agents.seen() {
		assert.NotContains(t, request.Prompt, "gh issue", "no agent is asked to close the issue")
	}
}

func TestRound_ARoundKilledAfterItsSpawnAndResumedSpawnsNothingAgain(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	client := rig.h.client(t, testVersion)

	var once sync.Once

	// The child runs its first agent after the round recorded the spawn. The kill is a cancel of the round at that point.
	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		once.Do(func() {
			if err := client.Cancel(testItem); err != nil {
				t.Error(err)
			}

			rig.h.tick(t)
		})

		return inner(request)
	}

	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, rig.file, modeOff())
	require.Error(t, err, "the cancel ends the first run of the round")

	handle := resumeRun(t, engine, client)

	waitOn(t, waiting[struct{}]{what: "the resumed run to show a question", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		infos, statusErr := client.Status()

		return struct{}{}, statusErr == nil && slices.ContainsFunc(infos, func(info RunInfo) bool { return info.Question != "" }), nil
	}})
	require.NoError(t, client.Reply(testItem, Reply{From: shipOwner, Text: "STOP"}))

	_, err = resultOfHandle(t, handle)
	require.NoError(t, err)

	spawns := 0

	for _, name := range stepNames(t, engine) {
		if strings.HasPrefix(name, "spawn:") {
			spawns++
		}
	}

	assert.Equal(t, 1, spawns, "the resumed round reads the recorded spawn and starts no second child")
	assert.Equal(t, 1, planRuns(rig), "the child of the item ran its plan once")
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the item landed once")
}

// planRuns counts the plan agents that the fake agents ran.
func planRuns(rig *roundRig) int {
	count := 0

	for _, request := range rig.h.agents.seen() {
		if request.Model == "opus" && request.Resume == "" && !strings.Contains(request.Prompt, reviewMarker) {
			count++
		}
	}

	return count
}
