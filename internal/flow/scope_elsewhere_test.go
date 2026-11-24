package flow

import (
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

const otherIssue model.IssueRef = "other-org/other-repo#5"

// askedAboutTheOtherIssue gates item 1, then gives its pull request a closing issue of another repository, and returns the rig and the question that the run shows.
func askedAboutTheOtherIssue(t *testing.T) (*batchRig, *Engine, <-chan runOutcome, string) {
	t.Helper()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12}}
	rig.h.forge.closingFar = [][]model.IssueRef{nil, {otherIssue}}
	engine := rig.engine(t)
	done := rig.runItemOne(t, engine)

	waitOn(t, ownerAsks(t, rig.h))

	infos := newStatusRead(rig.h.client(t, testVersion)).next()
	require.Len(t, infos, 1)

	return rig, engine, done, infos[0].Question
}

func TestMerge_AMemberWhoseBodyClosesAnIssueOfAnotherRepositoryMakesTheRunAskTheOwner(t *testing.T) {
	t.Parallel()

	rig, _, done, question := askedAboutTheOtherIssue(t)

	assert.Contains(t, question, "closes "+string(otherIssue)+" now")
	assert.Contains(t, question, "(then: #12)")
	assert.Contains(t, question, `for example "keep `+string(otherIssue)+`"`)

	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "keep other-org/other-repo#5"}))
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, []string{"1"}, rig.merger.landed())
	require.Len(t, rig.merger.given, 1)
	assert.Equal(t, []model.IssueRef{otherIssue}, rig.merger.given[0].Kept, "the merge guard may name the kept issue of another repository")
}

func TestMerge_TheNumberOfAnIssueOfAnotherRepositoryDoesNotKeepIt(t *testing.T) {
	t.Parallel()

	rig, _, done, _ := askedAboutTheOtherIssue(t)

	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "keep #5"}))
	require.NoError(t, receive(t, done).err)

	assert.Empty(t, rig.merger.landed(), "#5 is an issue of this repository, not other-org/other-repo#5")
	require.Len(t, rig.h.commands.ran(), 1)
	assert.Contains(t, rig.h.commands.ran()[0], string(otherIssue)+" has no owner line")
	assert.Contains(t, rig.h.commands.ran()[0], `"keep `+string(otherIssue)+`"`)
}

func TestMerge_AnIssueOfAnotherRepositoryThatTheFirstReviewHeldAsksNoQuestion(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12}}
	rig.h.forge.closingFar = [][]model.IssueRef{{otherIssue}, {otherIssue}}

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow(rig.scopeBody())), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestMerge_AnIssueOfAnotherRepositoryWithTheNumberOfAnOwnIssueOfTheFirstReviewIsNew(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{5}, {5}}
	rig.h.forge.closingFar = [][]model.IssueRef{nil, {otherIssue}}
	done := rig.runItemOne(t, rig.engine(t))

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "no"}))
	require.NoError(t, receive(t, done).err)

	assert.Empty(t, rig.merger.landed(), "#5 of this repository does not cover other-org/other-repo#5")
}

func TestScopeOutput_AScopeThatAnEarlierRunRecordedAsNumbersStillReadsAsOwnIssues(t *testing.T) {
	t.Parallel()

	old := dbos.StepInfo{StepName: "scope#1", Output: `{"found":true,"issues":[12,13]}`}
	assert.Equal(t, scopeRead{Found: true, Issues: []model.IssueRef{"#12", "#13"}}, scopeOutput(old))

	current := dbos.StepInfo{StepName: "scope#1", Output: `{"found":true,"issues":["#12","other-org/other-repo#5"]}`}
	assert.Equal(t, scopeRead{Found: true, Issues: []model.IssueRef{"#12", otherIssue}}, scopeOutput(current))
}

func TestKeptOutput_AnOwnerLineThatAnEarlierRunRecordedAsNumbersStillReadsAsOwnIssues(t *testing.T) {
	t.Parallel()

	old := dbos.StepInfo{StepName: "batch:b.kept#1", Output: `{"item":"1","issues":[13]}`}

	assert.Equal(t, keptRead{Item: "1", Issues: []model.IssueRef{"#13"}}, keptOutput(old))
}
