package flow

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// committingRig is the example rig whose agents commit once when their prompt holds one of the markers. heads lists the head after each commit, in order.
type committingRig struct {
	*agentRig

	mu    sync.Mutex
	heads []string
}

func newCommittingRig(t *testing.T, rig *agentRig, markers ...string) *committingRig {
	t.Helper()

	c := &committingRig{agentRig: rig}
	inner := rig.h.agents.result

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		for _, marker := range markers {
			if strings.Contains(request.Prompt, marker) {
				c.commit(t)

				break
			}
		}

		return inner(request)
	}

	return c
}

func (c *committingRig) commit(t *testing.T) {
	t.Helper()

	c.mu.Lock()
	defer c.mu.Unlock()

	commitIn(t, c.dir, fmt.Sprintf("change-%d.txt", len(c.heads)+1))
	head := strings.TrimSpace(gitIn(t, c.dir, "rev-parse", "HEAD"))
	c.heads = append(c.heads, head)

	// The gate of the rig judges the head that the last commit made, as a real gate judges the head it is given.
	// A rig with fewer gate results than commits reruns its last result, so that result takes the head too.
	c.h.gate.mu.Lock()
	for i := range c.h.gate.results {
		if i >= min(len(c.heads), len(c.h.gate.results))-1 {
			c.h.gate.results[i].Head = head
		}
	}
	c.h.gate.mu.Unlock()
}

// pushOf is the call that the fake forge records for a push of head to the branch of the test item with the lease expect.
func pushOf(head, expect string) string {
	return strings.Join([]string{testProject, testItem, testItem, head, expect}, "|")
}

func TestExamplePush_TheImplementAndTheFixPushTheirHeadsToTheBranchOfTheItemAndTheFixLeasesTheHeadBeforeIt(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "approve" })
	base.h.gate.results = []GateResult{{Passed: false, Head: base.head, Report: "lint: unused variable x"}, {Passed: true, Head: base.head, Tree: exampleTree}}
	rig := newCommittingRig(t, base, implementMarker, fixMarker)

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	require.Len(t, rig.heads, 2, "the implement step and one fix committed")
	assert.Equal(t, []string{pushOf(rig.heads[0], ""), pushOf(rig.heads[1], rig.heads[0])}, rig.h.forge.pushed,
		"the implement head goes to the branch of the item with the lease of the clone, and the fix head with the implement head as the lease")
}

func TestExamplePush_TheFirstPushIsTheHeadOfTheSweepAndNotTheHeadOfTheImplementStep(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "approve" })
	rig := newCommittingRig(t, base, implementMarker, sweepMarker)

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	require.Len(t, rig.heads, 2, "the implement step and the sweep committed")
	assert.Equal(t, []string{pushOf(rig.heads[1], "")}, rig.h.forge.pushed, "the branch of the item gets the head of the sweep, which holds the commit of the implement step")
}

func TestExamplePush_ASettleThatCommitsPushesItsHeadLeasedOnTheLoopHeadAndTheItemReportsThatHead(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "changes" })
	base.setRound("fix_rounds", oneFixRound)
	rig := newCommittingRig(t, base, settleMarker)
	done := pipelineInBackground(rig.h.engine(t), file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "fix all"}))
	outcome := receive(t, done)
	require.NoError(t, outcome.err)

	require.Len(t, rig.heads, 1, "only the settle step committed")
	require.NotEmpty(t, rig.h.forge.pushed)
	assert.Equal(t, pushOf(rig.heads[0], rig.head), rig.h.forge.pushed[len(rig.h.forge.pushed)-1], "the last push is the head of the settle, leased on the head that the loop ended at")

	settled := childOf(t, outcome.result)
	assert.Equal(t, "settled", settled.Reason)
	assert.Equal(t, rig.heads[0], settled.Head, "the settled item reports the head after the settle, which the branch holds")
}
