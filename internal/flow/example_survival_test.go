package flow

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
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
	assert.Contains(t, notes[0], "After this landing, the tip of main lacks lines that merges of the round added")

	sha := rig.repo.must(t, "rev-parse", "main^{/^land-lost$}")
	assert.Contains(t, notes[0], "| "+sha[:12]+" | land-lost | p.txt | 2 lines gone |", "the row names the merge by its first 12 characters, with the subject, the file and the count")
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

func TestExampleRound_ABatchThatMergedNothingReadsNoSurvivalEvenWhenAMergeOfTheRoundLostALine(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Lines: []string{"REFUSE: has the label hold"}, Refusal: []string{"REFUSE: has the label hold"}}}
	g := survivalOver(t, rig.batchRig)
	g.merge(t, "land-lost", "p.txt", "p1", "p2")
	g.drop(t, "p.txt", "p1")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the merge was asked for and refused")
	assert.Empty(t, integrationComments(rig), "the batch merged nothing, so the round reads no survival; the control with a merged item is the first test of this file")
}

func TestExampleRound_AHandOffRoundThatTheOwnerMergedReadsNoSurvival(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	g := survivalOver(t, rig.batchRig)
	g.merge(t, "land-lost", "p.txt", "p1", "p2")
	g.drop(t, "p.txt", "p1")
	rig.setRound("hand_off", true)
	rig.host.pr = &handForge{views: map[string][]fakePull{
		"1": {{State: "OPEN", Draft: true, Head: rig.heads["1"]}, {State: "OPEN", Head: rig.heads["1"]}, {State: "MERGED", Head: rig.heads["1"]}},
		"3": {{State: "OPEN", Draft: true, Head: rig.heads["3"]}, {State: "OPEN", Head: "beef"}},
	}, reads: map[string]int{}, draftErr: map[bool]error{}}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, integrationComments(rig), "the owner merged item 1, so the batch lists it as merged, and the round still reads no survival: the owner merges, the round lands nothing")
}

func TestExampleRound_ALossThatTheFirstLandingPostedIsNotPostedAgainAtTheNextLanding(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.h.opts.MaxConcurrent = 2
	rig.endTogether("3")

	g := survivalOver(t, rig.batchRig)
	g.merge(t, "land-lost", "p.txt", "p1", "p2")
	g.drop(t, "p.txt", "p1")

	// Item 1 lands first. The review of item 3 ends after that landing, and the merge below is a loss that only the second landing sees.
	rig.kids.onReview("3", func() {
		// The landing moved main with plumbing, so the folder of the repository still holds the old tree.
		rig.repo.must(t, "reset", "-q", "--hard")
		g.merge(t, "land-late", "q.txt", "q1", "q2")
		g.drop(t, "q.txt", "q1")
	})

	_, err := rig.run(t)

	require.NoError(t, err)
	require.Equal(t, []string{"1", "3"}, rig.merger.landed(), "two landings, one for each item")

	notes := integrationComments(rig)
	require.Len(t, notes, 2, "one comment for each landing")
	assert.Contains(t, notes[0], "land-lost")
	assert.NotContains(t, notes[0], "land-late")
	assert.Contains(t, notes[1], "land-late", "the second landing posts the loss that is new")
	assert.NotContains(t, notes[1], "land-lost", "and not the loss that the first landing posted")

	keyOf := func(note string) string { return strings.Split(note, "|")[2] }
	assert.NotEqual(t, keyOf(notes[0]), keyOf(notes[1]), "each landing has its own key, so the forge does not take the second comment for the first")
}

func TestExampleRound_ACommentShowsAtMostTheRowCapWithTheWholeCountAndAShortSubject(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "3")
	g := survivalOver(t, rig.batchRig)

	const merges = 45

	for i := range merges {
		subject := fmt.Sprintf("land-%02d", i)
		if i == 0 {
			subject = strings.Repeat("x", 100)
		}

		file := fmt.Sprintf("f%02d.txt", i)
		g.merge(t, subject, file, "keep", "gone")
		g.drop(t, file, "gone")
	}

	_, err := rig.run(t)

	require.NoError(t, err)

	notes := integrationComments(rig)
	require.Len(t, notes, 1)
	assert.Equal(t, 40, strings.Count(notes[0], " lines gone |"), "the comment holds the first 40 rows")
	assert.Contains(t, notes[0], "There are 45 new rows", "the count is the whole number")
	assert.Contains(t, notes[0], "5 more rows are not shown")
	assert.Contains(t, notes[0], "land-39")
	assert.NotContains(t, notes[0], "land-40", "a row after the cap is left out")
	assert.Contains(t, notes[0], strings.Repeat("x", 72)+"...", "a long subject is cut")
	assert.NotContains(t, notes[0], strings.Repeat("x", 73))
}

func TestExampleRound_AFailedGitCallOfTheSurvivalReadDoesNotFailTheRound(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "3")
	g := survivalOver(t, rig.batchRig)
	g.merge(t, "land-lost", "p.txt", "p1", "p2")
	g.drop(t, "p.txt", "p1")

	orig := rig.h.commands.git
	rig.h.commands.git = func(request CommandRequest) (CommandOutput, bool) {
		if len(request.Run) > 2 && request.Run[2] == survivalSetting && slices.Contains(request.Run, "rev-list") {
			return CommandOutput{Stderr: "boom\n", RC: 2}, true
		}

		return orig(request)
	}

	_, err := rig.run(t)

	require.NoError(t, err, "the batch landed and the read of the survival is a signal")
	assert.Equal(t, []string{"3"}, rig.merger.landed())
	assert.Empty(t, integrationComments(rig), "a read that failed posts nothing")
}

func TestExampleRound_AFailedPostOfTheSurvivalCommentDoesNotFailTheRound(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "3")
	rig.h.forge.writeErr = errFake
	g := survivalOver(t, rig.batchRig)
	g.merge(t, "land-lost", "p.txt", "p1", "p2")
	g.drop(t, "p.txt", "p1")

	_, err := rig.run(t)

	require.NoError(t, err, "the batch landed and the post of the survival comment is a signal")
	assert.Equal(t, []string{"3"}, rig.merger.landed())
	assert.Len(t, integrationComments(rig), 1, "the round tried the post once")
}
