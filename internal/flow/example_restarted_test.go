package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExampleRestarted_TheRoundLandsAnApprovedRunThatTheOwnerStartedWithBeehaveRun(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	engine := rig.engine(t)
	rig.last = engine

	// The owner runs the listed item by hand, as beehave run <item> does: a run that the round did not start. The review approves it.
	_, err := engine.RunFile("1", testProject, rig.file)
	require.NoError(t, err)
	require.Empty(t, rig.merger.landed(), "the run of one item lands nothing: only the round lands")

	_, err = rig.runOn(t, engine)

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the round lands the approved run once, as it lands the run of its own child")
	assert.Empty(t, heldOf(rig.writer), "a landed item is not put on hold")
	assert.Equal(t, []string{rig.heads["1"]}, rig.merger.pinned(), "the item lands at the head that its own review approved")
}

func TestExampleRestarted_ARoundThatLandsNothingLandsNoRestartedRunEither(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.setRound("merge", false)
	engine := rig.engine(t)
	rig.last = engine

	_, err := engine.RunFile("1", testProject, rig.file)
	require.NoError(t, err)

	_, err = rig.runOn(t, engine)

	require.NoError(t, err)
	assert.Empty(t, rig.merger.landed(), "vars.round.merge false lands no approved run, whoever started it")
}
