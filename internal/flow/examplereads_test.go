package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

// These tests hold the arguments that the shipped examples pass to the reads of the queue. A call that leaves one out reads every item or every pull request, and the rows stay right,
// so only the arguments that the tracker and the forge get can show it.

func TestExampleReads_TheQueueExamplePassesItsBaseToTheForge(t *testing.T) {
	t.Parallel()

	r := newLoopRig(t, queueItem("21", model.SeverityP0))

	require.NoError(t, r.run(t))

	assert.Contains(t, r.q.basesAsked, "integration", "the queue example reads the pull requests of the base that BASE names")
	assert.Empty(t, r.q.labelsAsked[0], "the queue example reads every open item")
}

func TestExampleReads_TheRemediationRoundPassesItsLabelToTheTracker(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Contains(t, rig.q.labelsAsked, "beehest-round", "the remediation round reads the items of the label of vars.round.label")
}
