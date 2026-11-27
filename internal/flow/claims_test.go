package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

// stopCode is the exit code of a stop that the library function claim_stop reports. It has the Code method of a preflight stop.
type stopCode int

// The codes of the claim stops.
const (
	stopDraft    stopCode = 8
	stopHeld     stopCode = 9
	stopClaimed  stopCode = 10
	stopAssigned stopCode = 11
)

func (c stopCode) Code() int { return int(c) }

// codeOfStop is the code of a stop of the preflight call or of claim_stop.
type codeOfStop interface{ Code() int }

// claimOutcome is what a run of claim_stop gave: the four fields of its result, the step names that it recorded, and the stop of the run.
type claimOutcome struct {
	line   string
	steps  []string
	stop   *Stopped
	issues []string
	err    error
}

// clearLine is the result of claim_stop for the open pull request 7 that nothing stops, and noPullLine the result for an item with no pull request.
// Both carry the reason that the preflight builtin gave before the claim stops moved to the library.
const (
	clearLine  = "True|UNCHANGED|0|OK: #7 is unchanged in every respect this checks."
	noPullLine = "True|UNCHANGED|0|OK: the item has no pull request yet."
)

// ownerLogins is the own dict of the claim tests.
const ownerLogins = `own = {"logins": ["owner", "bot"]}`

// openPull is the pull request 7 of the item: open, written by the owner, with nothing that stops it.
func pullFake(edit func(*model.PullFacts)) *readsFake {
	facts := model.PullFacts{Number: 7, State: "OPEN", Author: "owner"}
	if edit != nil {
		edit(&facts)
	}

	return &readsFake{facts: map[string]model.PullFacts{testItem: facts}, issues: map[string][]string{}}
}

// claimRun runs claim_stop with the arguments over the fake and reports what the call gave.
func claimRun(t *testing.T, fake *readsFake, args string) claimOutcome {
	t.Helper()

	h, w := readsHarness(t, fake)
	engine := h.engine(t)
	text := "load(\"@beehest//claims.star\", \"claim_stop\")\n" + bodyFlow(`c = claim_stop(`+args+`)
item_comment(id = "1", key = "k", text = "|".join([str(c.ok), c.stop, str(c.code), c.reason]))
`)

	result, err := engine.RunFile(testItem, testProject, writeFlow(t, text), modeOff())
	if err != nil {
		return claimOutcome{err: err}
	}

	require.Len(t, w.notes, 1)

	return claimOutcome{line: strings.TrimPrefix(w.notes[0], testProject+"|1|"+testRun+":k|"), steps: stepNames(t, engine), stop: result.Stopped, issues: fake.asked}
}

func TestClaimStop_AHeldLabelStopsWithCode9(t *testing.T) {
	t.Parallel()

	held := pullFake(func(f *model.PullFacts) { f.Labels = []string{"bug", "hold"} })

	got := claimRun(t, held, `name = "a", hold = "hold"`)

	require.NoError(t, got.err)
	assert.Equal(t, "False|HELD|9|STOP: #7 now carries the 'hold' label. A product decision is open; nothing merges.;   Recovery: write the judgement on the issue and move to the next item.", got.line)

	got = claimRun(t, held, `name = "a"`)

	assert.Equal(t, clearLine, got.line, "a call with no hold holds nothing")

	got = claimRun(t, held, `name = "a", hold = "holdup"`)

	assert.Equal(t, clearLine, got.line, "the whole label is compared")
}

func TestClaimStop_AForeignAuthorClaimsTheItemWithCode10(t *testing.T) {
	t.Parallel()

	foreign := pullFake(func(f *model.PullFacts) { f.Author = "someone" })

	got := claimRun(t, foreign, `name = "a", `+ownerLogins)

	require.NoError(t, got.err)
	assert.Equal(t, "False|CLAIMED|10|STOP: #7 is open and its author is 'someone', who is not on the list of own logins. The item is claimed.;   Recovery: leave the item to its author and move to the next item.", got.line)

	assert.Equal(t, clearLine, claimRun(t, foreign, `name = "a"`).line, "no own list checks no author")
	assert.Equal(t, clearLine, claimRun(t, pullFake(func(f *model.PullFacts) { f.Author = "Owner" }), `name = "a", `+ownerLogins).line, "forge logins ignore case")

	nobody := claimRun(t, pullFake(func(f *model.PullFacts) { f.Author = "" }), `name = "a", `+ownerLogins)

	assert.Contains(t, nobody.line, "its author is 'null'", "an author that the read lacks claims the item")
}

func TestClaimStop_AnAssigneeOutsideOwnStopsWithCode11(t *testing.T) {
	t.Parallel()

	assigned := pullFake(func(f *model.PullFacts) { f.Author = "someone"; f.Assignees = []string{"BOT", "maintainer"} })

	got := claimRun(t, assigned, `name = "a", `+ownerLogins)

	require.NoError(t, got.err)
	assert.Equal(t, "False|ASSIGNED|11|STOP: #7 is assigned to 'maintainer', who is not on the list of own logins. The item is claimed. knows=no;   Recovery: leave the item to the holder and move to the next item. To take it, ask the holder first.", got.line,
		"an assignee outranks a foreign author, and a login of the list is no holder")

	own := claimRun(t, pullFake(func(f *model.PullFacts) { f.Assignees = []string{"Owner"} }), `name = "a", `+ownerLogins)

	assert.Equal(t, clearLine, own.line)
	assert.Equal(t, clearLine, claimRun(t, assigned, `name = "a"`).line, "no own list checks no assignee")
}

func TestClaimStop_AnAssigneeOfALinkedIssueStopsAndKnowsIsRecorded(t *testing.T) {
	t.Parallel()

	linked := pullFake(func(f *model.PullFacts) {
		f.Links = []model.IssueLink{{Number: 3}, {Repo: "acme/tools", Number: 9}, {Number: 4}}
		f.Author = "owner"
		f.Reviewers = []string{"Reviewer"}
	})
	linked.issues = map[string][]string{"#3": {"owner"}, "acme/tools#9": {"reviewer"}}

	got := claimRun(t, linked, `name = "a", `+ownerLogins)

	require.NoError(t, got.err)
	assert.Equal(t, "False|ASSIGNED|11|STOP: the linked issue acme/tools#9 is assigned to 'reviewer', who is not on the list of own logins. The item is claimed. knows=yes;   Recovery: leave the item to the holder and move to the next item. To take it, ask the holder first.", got.line,
		"a holder that reviews the pull request is a holder that knows")
	assert.Equal(t, []string{"pull " + testItem, "issue #3", "issue acme/tools#9"}, got.issues, "the links are read in the order of the body and no further than the first holder")
	assert.Equal(t, []string{"pr_read:a#1", "issue_assignees:a-1#1", "issue_assignees:a-2#2", "item_stop:a#1"}, claimSteps(got.steps))

	linked.issues["acme/tools#9"] = []string{"stranger"}

	assert.Contains(t, claimRun(t, linked, `name = "a", `+ownerLogins).line, "knows=no")

	linked.facts[testItem] = withAuthor(linked.facts[testItem], "stranger")

	assert.Contains(t, claimRun(t, linked, `name = "a", `+ownerLogins).line, "knows=yes", "the holder that wrote the pull request knows")
}

func withAuthor(facts model.PullFacts, author string) model.PullFacts {
	facts.Author = author

	return facts
}

// claimSteps keeps the steps that claim_stop records, and drops the note that the test writes.
func claimSteps(steps []string) []string {
	var kept []string

	for _, step := range steps {
		if !strings.HasPrefix(step, "item_comment") {
			kept = append(kept, step)
		}
	}

	return kept
}

func TestClaimStop_ADraftIsAStopUnlessItIsTheOwnDraft(t *testing.T) {
	t.Parallel()

	draft := pullFake(func(f *model.PullFacts) { f.Draft = true })

	got := claimRun(t, draft, `name = "a"`)

	require.NoError(t, got.err)
	assert.Equal(t, "False|DRAFT|8|STOP: #7 is a DRAFT. It is not mergeable and may be mid-rework by its author.;   Recovery: review it if you were sent to review it, but do not merge and do not push;   without saying so on the pull request first.", got.line)

	assert.Equal(t, clearLine, claimRun(t, draft, `name = "a", own_draft = 7`).line, "the draft that the run opened is no stop")
	assert.Contains(t, claimRun(t, draft, `name = "a", own_draft = 8`).line, "DRAFT", "another draft is a stop")
}

func TestClaimStop_TheStopsKeepTheirOrder(t *testing.T) {
	t.Parallel()

	everything := func(edit func(*model.PullFacts)) string {
		fake := pullFake(func(f *model.PullFacts) {
			f.Author, f.Draft, f.Labels, f.Assignees = "someone", true, []string{"hold"}, []string{"maintainer"}
			edit(f)
		})

		return claimRun(t, fake, `name = "a", hold = "hold", `+ownerLogins).line
	}

	assert.True(t, strings.HasPrefix(everything(func(*model.PullFacts) {}), "False|ASSIGNED|11|"), "assigned comes first")
	assert.True(t, strings.HasPrefix(everything(func(f *model.PullFacts) { f.Assignees = nil }), "False|CLAIMED|10|"), "claimed comes second")
	assert.True(t, strings.HasPrefix(everything(func(f *model.PullFacts) { f.Assignees, f.Author = nil, "owner" }), "False|HELD|9|"), "held comes third")
	assert.True(t, strings.HasPrefix(everything(func(f *model.PullFacts) { f.Assignees, f.Author, f.Labels = nil, "owner", nil }), "False|DRAFT|8|"), "the draft comes last")
}

func TestClaimStop_AFailedReadIsAnErrorAndNeverAPass(t *testing.T) {
	t.Parallel()

	down := pullFake(nil)
	down.err = errFake

	got := claimRun(t, down, `name = "a", `+ownerLogins)

	require.ErrorIs(t, got.err, errFake)

	linked := pullFake(func(f *model.PullFacts) { f.Links = []model.IssueLink{{Repo: "acme/tools", Number: 9}} })
	linked.failIssue = true

	got = claimRun(t, linked, `name = "a", `+ownerLogins)

	require.ErrorIs(t, got.err, errFake)
	assert.ErrorContains(t, got.err, "cannot read the assignees of acme/tools#9")
}

func TestClaimStop_AMissingAssigneeOrLabelListIsRefused(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"labels", "assignees", "draft"} {
		fake := pullFake(nil)
		fake.err = &model.IncompletePull{Facts: model.PullFacts{Number: 7}, Missing: []string{field}}

		got := claimRun(t, fake, `name = "a", `+ownerLogins)

		var hole *model.IncompletePull

		require.ErrorAs(t, got.err, &hole, field)
		assert.ErrorContains(t, got.err, "the read has no "+field)
	}
}

func TestClaimStop_AnItemWithNoPullRequestPasses(t *testing.T) {
	t.Parallel()

	assert.Equal(t, noPullLine, claimRun(t, &readsFake{}, `name = "a", hold = "hold", `+ownerLogins).line)

	for _, state := range []string{"MERGED", "CLOSED"} {
		gone := pullFake(func(f *model.PullFacts) {
			f.State, f.Author, f.Labels, f.Draft = state, "someone", []string{"hold"}, true
		})

		got := claimRun(t, gone, `name = "a", hold = "hold", `+ownerLogins)

		assert.Equal(t, clearLine, got.line, "a pull request that is "+state+" is for the preflight call to report")
		assert.Nil(t, got.stop)
	}
}

func TestClaimStop_AStopIsRecordedOnTheResultOfTheRun(t *testing.T) {
	t.Parallel()

	held := pullFake(func(f *model.PullFacts) { f.Labels = []string{"hold"} })

	got := claimRun(t, held, `name = "a", hold = "hold"`)

	require.NoError(t, got.err)
	require.NotNil(t, got.stop)
	assert.Equal(t, "claim_stop:a", got.stop.Step)
	assert.Equal(t, 9, got.stop.RC)
	assert.Contains(t, got.stop.Output, "now carries the 'hold' label")

	other := claimRun(t, pullFake(func(f *model.PullFacts) { f.Labels = []string{"hold"} }), `name = "a", hold = "hold", id = "other-item"`)

	assert.Equal(t, noPullLine, other.line, "the item other-item has no pull request in the fake")
	assert.Nil(t, other.stop)

	clean := claimRun(t, pullFake(nil), `name = "a", hold = "hold", `+ownerLogins)

	assert.Nil(t, clean.stop, "a clear result records no stop")
	assert.Equal(t, []string{"pr_read:a#1"}, claimSteps(clean.steps))
}

func TestClaimStop_AStopOfAnotherItemIsNoStopOfTheRun(t *testing.T) {
	t.Parallel()

	fake := pullFake(nil)
	fake.facts["other-item"] = model.PullFacts{Number: 9, State: "OPEN", Author: "owner", Labels: []string{"hold"}}

	got := claimRun(t, fake, `name = "a", hold = "hold", id = "other-item"`)

	require.NoError(t, got.err)
	assert.Contains(t, got.line, "False|HELD|9|")
	assert.Nil(t, got.stop, "the merge round reads other items, and their stops are no stop of the run")
	assert.Equal(t, []string{"pull other-item"}, got.issues)
}

func TestClaimStop_RefusesAnOwnThatIsNoDictAndAnOwnDraftThatIsNoNumber(t *testing.T) {
	t.Parallel()

	for name, args := range map[string]string{
		"own is a list":     `name = "a", own = ["owner"]`,
		"own_draft is text": `name = "a", own_draft = "7"`,
		"own_draft below 0": `name = "a", own_draft = -1`,
	} {
		fake := pullFake(nil)

		got := claimRun(t, fake, args)

		require.ErrorContains(t, got.err, "REFUSE: claim_stop got", name)
		assert.Empty(t, fake.asked, "a refused call reads nothing")
	}
}
