package flow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/batch"
	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/preflight"
)

// remoteBranches is a preflight reader over the branches on the remote: the head of each branch, and no entry for a branch that is gone.
// It gives the stops that the real check gives for a branch that is gone and for a head that is not the head the call expects.
type remoteBranches struct {
	mu    sync.Mutex
	heads map[string]string
	asked []string
}

func (r *remoteBranches) Preflight(_ context.Context, _, item string, req preflight.Request, _ string) (preflight.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.asked = append(r.asked, item)

	head, present := r.heads[item]

	switch {
	case !present:
		return preflight.Result{Stop: preflight.BranchGone, Lines: []string{"STOP: the head branch of item " + item + " no longer exists"}}, nil
	case req.ExpectHead != "" && req.ExpectHead != head:
		return preflight.Result{Stop: preflight.HeadMoved, Lines: []string{"STOP: head moved to " + head}}, nil
	}

	return preflight.Result{Stop: preflight.Unchanged}, nil
}

// landingExample returns the batch rig with the environment of the example and the workflow file of the repository, unchanged.
// pins is the LAND value, switchValue is the MERGE switch, or "" to leave it unset. The remote holds the head of each member.
func landingExample(t *testing.T, pins func(heads map[string]string) string, switchValue string) (*batchRig, string, *fakeWriter, *remoteBranches) {
	t.Helper()

	rig := newBatchRig(t)
	folder := t.TempDir()
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", "remediation"))))

	rig.h.opts.Env = map[string]string{"REPO": "o/r", "BASE": "main", "OWNER_LOGIN": shipOwner, "OWNER_EMAIL": shipMail, "LAND": pins(rig.heads)}
	if switchValue != "" {
		rig.h.opts.Env["MERGE"] = switchValue
	}

	remote := &remoteBranches{heads: map[string]string{}}
	for item, head := range rig.heads {
		remote.heads[item] = head
	}

	rig.h.preflight = remote

	writer := newFakeWriter()
	rig.h.writer = writer

	return rig, filepath.Join(folder, "workflow.star"), writer, remote
}

// reviewed pins the listed items at the head each has in the repository.
func reviewed(items string) func(map[string]string) string {
	return func(heads map[string]string) string {
		var pins []string

		for _, item := range strings.Fields(items) {
			pins = append(pins, item+"="+heads[item])
		}

		return strings.Join(pins, " ")
	}
}

func TestExampleBatch_TwoReadyItemsLandInOneBatchWithOneGateRun(t *testing.T) {
	t.Parallel()

	rig, file, _, _ := landingExample(t, reviewed("1 3"), "true")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
	assert.Len(t, rig.gate.ran(), 1, "two green members take one gate run")
	assert.Equal(t, "README\none.txt\nthree.txt", rig.mainFiles(t))

	for _, given := range rig.merger.given {
		assert.Equal(t, "hold", given.Hold, "the guards of the merge of one item apply")
		assert.Equal(t, []string{shipOwner}, given.Logins)
		assert.Equal(t, []string{shipMail}, given.Emails)
		assert.Len(t, given.ForbidText, 4)
	}
}

func TestExampleBatch_AnItemThatAGuardRefusesIsHeldAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig, file, writer, _ := landingExample(t, reviewed("1 3"), "true")
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Lines: []string{"REFUSE: has the label hold"}}}

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, "README\nthree.txt", rig.mainFiles(t), "item 3 lands and item 1 does not")
	assert.Equal(t, []string{testProject + "|1|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "REFUSE: has the label hold")
}

func TestExampleBatch_APushAfterTheReviewRefusesOnlyThatItem(t *testing.T) {
	t.Parallel()

	rig, file, writer, remote := landingExample(t, reviewed("1 3"), "true")
	remote.heads["1"] = strings.Repeat("e", 40) // somebody pushed to the branch of item 1 after the owner reviewed it

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the pushed item never reaches the merge")
	assert.Equal(t, "README\nthree.txt", rig.mainFiles(t))
	assert.Equal(t, []string{testProject + "|1|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "head moved")
}

func TestExampleBatch_AMemberLandsAtTheHeadTheOwnerReviewedAndNotTheHeadOnTheRemote(t *testing.T) {
	t.Parallel()

	rig, file, _, _ := landingExample(t, reviewed("1 3"), "true")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{rig.heads["1"], rig.heads["3"]}, rig.merger.pinned(), "each member merges at its pin")
}

func TestExampleBatch_AMissingBranchIsReportedAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig, file, writer, remote := landingExample(t, reviewed("1 3"), "true")
	delete(remote.heads, "1")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed())
	assert.Equal(t, []string{testProject + "|1|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "no longer exists")
}

func TestExampleBatch_AnItemWithNoChangelogEntryIsHeldWhenTheProjectDeclaresOneAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig, file, writer, _ := landingExample(t, reviewed("1 3"), "true")
	rig.h.facts = func(project string) model.Project { return model.Project{Name: project, Changelog: "CHANGELOG.md"} }
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[1] == "diff" && strings.HasSuffix(request.Run[len(request.Run)-1], rig.heads["1"]) {
			return CommandOutput{Stdout: "one.txt\n"}, nil
		}

		return CommandOutput{Stdout: "three.txt\nCHANGELOG.md\n"}, nil
	}

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the item with no entry never reaches the merge")
	assert.Equal(t, []string{testProject + "|1|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "the project declares the changelog CHANGELOG.md")
	assert.Equal(t, []string{
		"git fetch origin " + rig.heads["1"], "git diff --name-only origin/main..." + rig.heads["1"],
		"git fetch origin " + rig.heads["3"], "git diff --name-only origin/main..." + rig.heads["3"],
	}, rig.h.commands.ran(), "each member is fetched, then read from the base to its own head")
}

func TestExampleBatch_TheChangelogChecksOfTwoItemsRecordTheirOwnSteps(t *testing.T) {
	t.Parallel()

	rig, file, _, _ := landingExample(t, reviewed("1 3"), "true")
	rig.h.facts = func(project string) model.Project { return model.Project{Name: project, Changelog: "CHANGELOG.md"} }
	rig.h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{Stdout: "CHANGELOG.md\n"}, nil }
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)

	require.NoError(t, err)

	joined := strings.Join(stepNames(t, engine), " ")
	for _, id := range []string{"1", "3"} {
		assert.Contains(t, joined, "changelog-fetch-"+id+"#")
		assert.Contains(t, joined, "changed-paths-"+id+"#")
	}
}

func TestExampleBatch_AnItemWhoseHeadCannotBeFetchedIsHeldWithTheReasonAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig, file, writer, _ := landingExample(t, reviewed("1 3"), "true")
	rig.h.facts = func(project string) model.Project { return model.Project{Name: project, Changelog: "CHANGELOG.md"} }
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[1] == "fetch" && request.Run[len(request.Run)-1] == rig.heads["1"] {
			return CommandOutput{RC: 1, Stderr: "fatal: no such ref\n"}, nil
		}

		return CommandOutput{Stdout: "CHANGELOG.md\n"}, nil
	}

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err, "a code of 1 is a result, not a stop of the run")
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the item that cannot be checked never reaches the merge")
	assert.Equal(t, []string{testProject + "|1|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "could not be fetched")
	assert.Contains(t, writer.notes[0], "fatal: no such ref")
	assert.NotContains(t, rig.h.commands.ran(), "git diff --name-only origin/main..."+rig.heads["1"], "no diff runs after a failed fetch")
}

func TestExampleBatch_AnItemWhoseChangedPathsCannotBeReadIsHeldWithTheReasonAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig, file, writer, _ := landingExample(t, reviewed("1 3"), "true")
	rig.h.facts = func(project string) model.Project { return model.Project{Name: project, Changelog: "CHANGELOG.md"} }
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[1] == "diff" && strings.HasSuffix(request.Run[len(request.Run)-1], rig.heads["1"]) {
			return CommandOutput{RC: 1, Stderr: "fatal: bad revision\n"}, nil
		}

		return CommandOutput{Stdout: "CHANGELOG.md\n"}, nil
	}

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err, "a code of 1 is a result, not a stop of the run")
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the item that cannot be checked never reaches the merge")
	assert.Equal(t, []string{testProject + "|1|hold"}, writer.statuses)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "changed paths of item 1 could not be read")
	assert.Contains(t, writer.notes[0], "fatal: bad revision")
}

func TestExampleBatch_EveryItemGetsTheStopsOfOneItemBeforeTheBatch(t *testing.T) {
	t.Parallel()

	rig, file, _, remote := landingExample(t, reviewed("1 3"), "true")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, remote.asked)
}

func TestExampleBatch_APinThatIsNotAnIdAndAShaFailsTheRun(t *testing.T) {
	t.Parallel()

	rig, file, _, _ := landingExample(t, func(map[string]string) string { return "1" }, "true")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not id=sha")
	assert.Empty(t, rig.merger.asked)
}

func TestExampleBatch_AMergeSwitchThatIsNotSetFailsTheRunAsForOneItem(t *testing.T) {
	t.Parallel()

	rig, file, writer, remote := landingExample(t, reviewed("1 3"), "")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err, "a run fails if MERGE is not set, as the pipeline of one item does")
	assert.Empty(t, rig.merger.asked)
	assert.Empty(t, rig.gate.ran())
	assert.Empty(t, remote.asked)
	assert.Empty(t, writer.statuses)
	assert.Equal(t, "README", rig.mainFiles(t))
}

func TestExampleBatch_AMergeSwitchThatIsNotTrueLandsNothing(t *testing.T) {
	t.Parallel()

	rig, file, writer, remote := landingExample(t, reviewed("1 3"), "false")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Empty(t, rig.merger.asked, "no member reaches the merge")
	assert.Empty(t, rig.gate.ran(), "no gate runs")
	assert.Empty(t, remote.asked, "no branch is read")
	assert.Empty(t, writer.statuses)
	assert.Equal(t, "README", rig.mainFiles(t))
}

func TestExampleBatch_OneReadyItemLandsAsABatchOfOne(t *testing.T) {
	t.Parallel()

	rig, file, _, _ := landingExample(t, reviewed("3"), "true")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed())
}

func TestExampleBatch_TheIntegrationPullRequestOfTheEnvironmentReachesTheReadAfterTheBatch(t *testing.T) {
	t.Parallel()

	rig, file, _, _ := landingExample(t, reviewed("1 3"), "true")
	rig.h.opts.Env["INTEGRATION_PR"] = "7"

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + " main 7 1,3"}, rig.host.issueReads())
}

func TestExampleBatch_ARunWithNoIntegrationPullRequestReadsWithNumberZero(t *testing.T) {
	t.Parallel()

	rig, file, _, _ := landingExample(t, reviewed("1 3"), "true")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + " main 0 1,3"}, rig.host.issueReads())
}

func TestExampleBatch_AFindingStopsTheRunWithARefuseForTheOwner(t *testing.T) {
	t.Parallel()

	rig, file, _, _ := landingExample(t, reviewed("1 3"), "true")
	rig.host.report = batch.Report{Findings: []string{"issue #1 of item 1 is not closed after the batch landed"}}

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "issue #1 of item 1 is not closed after the batch landed")
	assert.Contains(t, err.Error(), "by hand")
}

func TestExampleBatch_AnUnprovenMemberStopsTheRunWithARefuseThatNamesTheItemAndTheReason(t *testing.T) {
	t.Parallel()

	rig, file, _, _ := landingExample(t, reviewed("1 3"), "true")
	rig.host.unproven = []batch.Outcome{{Item: "3", Reason: "its branch holds another commit now"}}

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "item 3: its branch holds another commit now")
}

func TestExampleBatch_ANoteIsCommentedAsAnIssueCheckThatDidNotRun(t *testing.T) {
	t.Parallel()

	rig, file, writer, _ := landingExample(t, reviewed("1 3"), "true")
	rig.host.report = batch.Report{Note: "the base is not the default branch"}

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "the issue check did not run: the base is not the default branch")
}

func TestExampleBatch_AReportWithNoFindingAndNoNoteSaysNothing(t *testing.T) {
	t.Parallel()

	rig, file, writer, _ := landingExample(t, reviewed("1 3"), "true")

	_, err := rig.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Empty(t, writer.notes)
}

func TestExampleSwarm_TheLandCallPassesTheIntegrationAndActsOnFindingsAndNote(t *testing.T) {
	t.Parallel()

	text := string(mustRead(t, filepath.Join("..", "..", "examples", "swarm", "workflow.star")))

	assert.Contains(t, text, `base = env("BASE")`)
	assert.Contains(t, text, `repo = env("REPO")`)
	assert.Contains(t, text, "out.merged")
	assert.Contains(t, text, "out.rejected")
	assert.Contains(t, text, "out.deferred")
	assert.Contains(t, text, `"Not landed: " + rejected.reason`)
	assert.Contains(t, text, "the forge could not settle")
}
