package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// amendRig makes every agent call that matches amend rewrite the commit it started on.
func amendRig(t *testing.T, verdicts func(review int) string, amend func(AgentRequest) bool) (*agentRig, string) {
	t.Helper()

	rig, file := exampleRig(t, verdicts)
	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)
		if amend(request) {
			gitIn(t, rig.dir, "commit", "-q", "--allow-empty", "--amend", "-m", "amended")
		}

		return result
	}

	return rig, file
}

func TestExample_AnImplementThatRewroteHistoryStopsTheItemBeforeTheGate(t *testing.T) {
	t.Parallel()

	rig, file := amendRig(t, func(int) string { return "approve" }, func(r AgentRequest) bool { return r.Model == "sonnet" && r.Resume == "" })

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "rewrote the history")
	assert.Zero(t, rig.h.gate.calls, "no gate runs on a rewritten head")
}

func TestExample_AFixThatRewroteHistoryStopsTheItemBeforeTheNextGate(t *testing.T) {
	t.Parallel()

	rig, file := amendRig(t, func(int) string { return "changes" }, func(r AgentRequest) bool { return r.Resume != "" })

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "the fix step rewrote the history")
	assert.Equal(t, 1, rig.h.gate.calls, "the gate of the first round is the only one")
}
