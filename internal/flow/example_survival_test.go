package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// integrationComments returns the comments that the run posted on a pull request by its number: the comments of the item itself carry no number.
func integrationComments(rig *roundRig) []string {
	rig.h.forge.mu.Lock()
	defer rig.h.forge.mu.Unlock()

	var notes []string

	for _, note := range rig.h.forge.notes {
		if strings.Contains(note, "#pr") {
			notes = append(notes, note)
		}
	}

	return notes
}

func TestExampleRound_ALandingThatLostALineLaterCommentsItOnTheIntegrationPullRequest(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "3")
	g := survivalOver(t, rig.batchRig)
	g.merge(t, "land-lost", "p.txt", "p1", "p2", "p3")
	g.merge(t, "land-kept", "q.txt", "q1")
	g.drop(t, "p.txt", "p1", "p3")

	_, err := rig.run(t)

	require.NoError(t, err, "a lost line is a signal and the round goes on")
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the landing itself is not stopped")

	notes := integrationComments(rig)
	require.Len(t, notes, 1, "one comment for the one landing")
	assert.Contains(t, notes[0], testProject+"|"+testItem+"#pr9|", "the comment goes to pull request 9, which the round integration step returned")
	assert.Contains(t, notes[0], "land-lost")
	assert.Contains(t, notes[0], "p.txt")
	assert.Contains(t, notes[0], "2 lines gone")
	assert.NotContains(t, notes[0], "land-kept", "a merge that lost nothing has no row")
}

func TestExampleRound_ALandingThatLostAWholeFileListsTheFileApartAndTheRoundGoesOn(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "3")
	g := survivalOver(t, rig.batchRig)
	g.merge(t, "land-gone", "d.txt", "d1")
	g.remove(t, "d.txt")

	_, err := rig.run(t)

	require.NoError(t, err)

	notes := integrationComments(rig)
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], "| land-gone | d.txt | file gone |")
}

func TestExampleRound_ALandingThatLostNothingPostsNoCommentOnTheIntegrationPullRequest(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "3")
	g := survivalOver(t, rig.batchRig)
	g.merge(t, "land-kept", "q.txt", "q1", "q2")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed())
	assert.Empty(t, integrationComments(rig), "the same round with every line kept posts nothing; the first test of this file is the control with a loss")
}
