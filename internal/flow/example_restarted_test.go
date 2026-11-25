package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
)

func TestExampleRestarted_TheRoundLandsAnApprovedRunThatTheOwnerStartedWithBeehaveRun(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.answers = []string{"CONTINUE", "STOP"} // the round waits, and the owner asks for one more pass
	engine := rig.engine(t)
	rig.last = engine

	// The owner runs the listed item by hand, as beehave run <item> does: a run that the round did not start. The review approves it.
	_, err := engine.RunFile("1", testProject, rig.file, modeOff())
	require.NoError(t, err)
	require.Empty(t, rig.merger.landed(), "the run of one item lands nothing: only the round lands")

	_, err = rig.runOn(t, engine)

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the round lands the approved run once across its passes: the landing closes the item, so the next pass does not offer it")
	assert.Empty(t, heldOf(rig.writer), "a landed item is not put on hold")
	assert.Equal(t, []string{rig.heads["1"]}, rig.merger.pinned(), "the item lands at the head that its own review approved")
}

func TestExampleRestarted_ARoundThatLandsNothingLandsNoRestartedRunEither(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.setRound("merge", false)
	engine := rig.engine(t)
	rig.last = engine

	_, err := engine.RunFile("1", testProject, rig.file, modeOff())
	require.NoError(t, err)

	_, err = rig.runOn(t, engine)

	require.NoError(t, err)
	assert.Empty(t, rig.merger.landed(), "vars.round.merge false lands no approved run, whoever started it")
}

func TestExampleRestarted_ARunThatDoesNotApproveAHeadIsNotLanded(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"the review did not approve":     `{"approved": False, "head": "HEAD"}`,
		"the verdict is not a bool true": `{"approved": "yes", "head": "HEAD"}`,
		"the run has no head":            `{"approved": True, "head": ""}`,
		"the run names no head at all":   `{"approved": True}`,
		"the value is not a dict":        `["approved"]`,
		"the run returned nothing":       `None`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t, "1")
			engine := rig.engine(t)
			rig.last = engine

			flow := "def main(item):\n    return " + strings.ReplaceAll(body, "HEAD", rig.heads["1"]) + "\n"
			_, err := engine.RunFile("1", testProject, writeFlow(t, flow), modeOff())
			require.NoError(t, err)

			_, err = rig.runOn(t, engine)

			require.NoError(t, err)
			assert.Empty(t, rig.merger.landed(), "a done row with no approved head is not landed")
			assert.Empty(t, heldOf(rig.writer), "and it is not held: the round never offered it")
		})
	}
}

func TestExampleRestarted_AChildThatEndedInAPassLandsOnceInThePassAfter(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.answers = []string{"CONTINUE", "STOP"}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the child lands once, from the queue, across the passes of the round")

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.NotContains(t, joined, "round-land-1.", "the round lands from the queue read of the pass after the child ended, not from the gather of that pass")
	assert.Contains(t, joined, "gate:round-land-2.gate#")
}

func TestExampleRestarted_AnItemThatALandingRejectedIsOfferedOnce(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.answers = []string{"CONTINUE", "STOP"}
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Lines: []string{"REFUSE: has the label hold"}, Refusal: []string{"REFUSE: has the label hold"}}}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the merge was asked once: the rejection put the item on hold, and a row on hold is not done")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer), "one hold status")
	assert.Len(t, notesWithKey(rig.writer, "rejected"), 1, "one stop comment")
}
