package flow

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

// gitWord is the program of the git commands that the batch library runs.
const gitWord = "git"

// reasonMoved is the reason of a member that a stop left unlanded because the base moved.
const reasonMoved = "the base moved while the batch ran; run the batch again"

// noLandingNote is the note of a batch that landed nothing.
const noLandingNote = "issue states: no member landed, so no read ran."

// libRig is a batch rig whose command runner runs the git commands of the library in the repository of the rig, and whose forge reads every issue as CLOSED.
func libRig(t *testing.T) *batchRig {
	t.Helper()

	rig := newBatchRig(t)
	rig.runGit()
	rig.h.queue = &fakeQueue{issueStates: map[int]string{0: issueClosed, 1: issueClosed, 2: issueClosed, 3: issueClosed, 4: issueClosed}}

	return rig
}

// runGit makes the command runner of the rig run each git command in the repository, as the worktree of the item does, and answer any other program with rc 0.
func (r *batchRig) runGit() {
	r.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) == 0 || request.Run[0] != gitWord {
			return CommandOutput{}, nil
		}

		res := r.repo.Run(context.Background(), request.Run[1:])

		return CommandOutput{Stdout: res.Stdout, Stderr: res.Stderr, RC: res.ExitCode}, nil
	}
}

// batchIdentity is the word after `git -c` in each git command of the batch library: the author of its union commits.
const batchIdentity = "user.name=beehave"

// libraryGit runs a git command of the batch library in the repository of the rig, as the worktree of the round item does. It answers no other command.
func (r *batchRig) libraryGit(request CommandRequest) (CommandOutput, bool) {
	if len(request.Run) < 3 || request.Run[0] != gitWord || request.Run[2] != batchIdentity {
		return CommandOutput{}, false
	}

	res := r.repo.Run(context.Background(), request.Run[1:])

	return CommandOutput{Stdout: res.Stdout, Stderr: res.Stderr, RC: res.ExitCode}, true
}

// libCall writes a call of batch_merge over the listed items and a report command with the lists of the result. extra holds more keywords, each followed by a comma.
func (r *batchRig) libCall(items, extra string) string {
	var prs []string

	for _, item := range strings.Fields(items) {
		prs = append(prs, `struct(item = "`+item+`", head = "`+r.heads[item]+`", closes = [])`)
	}

	return `load("@beehave//batch.star", "batch_merge")

def main(item):
    out = batch_merge(prs = [` + strings.Join(prs, ", ") + `], base = "main", repo = "o/r", ` + extra + `name = "b")
    command(name = "report", run = ["report", ",".join(out.merged), ",".join([r.item + ":" + r.reason for r in out.rejected]), ",".join([d.item + ":" + d.reason for d in out.deferred]), str(out.gates), "|".join(out.findings), out.note])
`
}

// libWalk runs the workflow text for the test item.
func (r *batchRig) libWalk(t *testing.T, text string) error {
	t.Helper()

	_, err := r.engine(t).RunFile(testItem, testProject, writeFlow(t, text), modeOff())

	return err
}

// libReport is the one report line of the run.
func (r *batchRig) libReport(t *testing.T) string {
	t.Helper()

	lines := filterReport(r.h.commands.ran())
	require.Len(t, lines, 1, "one report: %v", r.h.commands.ran())

	return lines[0]
}

// branchOn makes a branch off main that writes each file with its text, and returns its head.
func (r *batchRig) branchOn(t *testing.T, branch string, files map[string]string) string {
	t.Helper()

	r.repo.must(t, "switch", "-q", "main")
	r.repo.must(t, "switch", "-q", "-c", branch)

	for _, file := range slices.Sorted(maps.Keys(files)) {
		require.NoError(t, os.WriteFile(filepath.Join(r.repo.dir, file), []byte(files[file]), 0o600))
		r.repo.must(t, "add", file)
	}

	r.repo.must(t, "commit", "-q", "-m", branch)
	head := r.repo.must(t, "rev-parse", "HEAD")
	r.repo.must(t, "switch", "-q", "main")

	return head
}

// mainTree is the tree of main in the repository of the rig.
func (r *batchRig) mainTree(t *testing.T) string {
	t.Helper()

	return r.repo.must(t, "rev-parse", "main^{tree}")
}

func TestReviewFix_ABatchLandsItsMembersInOrderAndGatesTheUnion(t *testing.T) {
	t.Parallel()

	rig := libRig(t)

	require.NoError(t, rig.libWalk(t, rig.libCall("3 2 1", "")))

	assert.Equal(t, []string{"1", "3"}, rig.merger.landed(), "the green members land in item order")
	assert.Equal(t, "README\none.txt\nthree.txt", rig.mainFiles(t), "main holds the two green members and not bad.txt")
	assert.Equal(t, "report 1,3 2:red on top of the merged members  4  ", rig.libReport(t))
	assert.Equal(t, []string{"beehave/gate success " + rig.heads["1"], "beehave/gate success " + rig.heads["3"]}, rig.merger.statusesGiven())

	gated := rig.gate.ran()
	require.Len(t, gated, 4, "the union, its first half, member 1, then member 3 on top: the red tree of 2 on top of 1 is not gated again")
	assert.Equal(t, rig.mainTree(t), gated[3], "the last gate ran on the tree that landed")
}

func TestBatchLibrary_TwoGreenMembersLandWithOneGateAndOneMergeCall(t *testing.T) {
	t.Parallel()

	rig := libRig(t)

	require.NoError(t, rig.libWalk(t, rig.libCall("1 3", "")))

	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
	assert.Len(t, rig.gate.ran(), 1, "one gate for the whole union")
	assert.Equal(t, "report 1,3   1  ", rig.libReport(t))
	assert.Len(t, rig.host.proofReads(), 1, "one proof for the batch")
}

func TestBatchLibrary_AHeadThatTheRepositoryLacksIsRejectedAndTheRestLand(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	rig.heads["2"] = strings.Repeat("ab", 20)

	require.NoError(t, rig.libWalk(t, rig.libCall("1 2", "")))

	assert.Equal(t, "report 1 2:head "+rig.heads["2"]+" of 2 is not in the repository; fetch it  1  ", rig.libReport(t))
}

func TestBatchLibrary_AMemberThatConflictsWithTheBaseIsRejected(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	rig.heads["4"] = rig.branchOn(t, "m4", map[string]string{"README": "other\n"})
	readme := rig.branchOn(t, "readme", map[string]string{"README": "main\n"})
	rig.repo.must(t, "update-ref", "refs/heads/main", readme)

	require.NoError(t, rig.libWalk(t, rig.libCall("1 4", "")))

	assert.Equal(t, "report 1 4:conflict  1  ", rig.libReport(t))
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestBatchLibrary_AMemberThatConflictsWithAnEarlierMemberIsRetriedOnceOnTheNewTip(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	rig.heads["4"] = rig.branchOn(t, "m4", map[string]string{"one.txt": "other\n"})

	require.NoError(t, rig.libWalk(t, rig.libCall("1 4", "")))

	assert.Equal(t, "report 1 4:conflicts with 1  1  ", rig.libReport(t), "the retry folds 4 on the tip that 1 made, where it conflicts with 1")
}

func TestBatchLibrary_AHeldMemberIsGatedAloneInTheRetry(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	rig.heads["4"] = rig.branchOn(t, "m4", map[string]string{"bad.txt": "other\n"})

	require.NoError(t, rig.libWalk(t, rig.libCall("2 4", "")))

	assert.Equal(t, "report  2:red,4:red  2  "+noLandingNote, rig.libReport(t), "4 is held with 2, then gated alone in the retry")
}

func TestBatchLibrary_AConflictInTheRetryWithAMemberThatDidNotLandNamesTheBase(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	rig.heads["0"] = rig.branchOn(t, "m0", map[string]string{"bad.txt": "zero\n"})
	rig.heads["4"] = rig.branchOn(t, "m4", map[string]string{"bad.txt": "four\n", "one.txt": "four\n"})

	require.NoError(t, rig.libWalk(t, rig.libCall("0 1 4", "")))

	assert.Equal(t, "report 1 0:red,4:conflicts with base  3  ", rig.libReport(t), "4 is held with 0, which is red, and conflicts with the tip that 1 made in the retry")
}

func TestBatchLibrary_ATransientRefusalDefersTheMemberAndTheRest(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Reason: merge.Transient, Lines: []string{"REFUSE: checks run"}, Refusal: []string{"REFUSE: checks run"}}}

	require.NoError(t, rig.libWalk(t, rig.libCall("1 3", "")))

	assert.Equal(t, "report   1:REFUSE: checks run,3:REFUSE: checks run 1  "+noLandingNote, rig.libReport(t))
}

func TestBatchLibrary_ARefusedMemberIsRejectedAndTheRestAreGatedAgain(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Reason: merge.Plain, Lines: []string{"REFUSE: hold"}, Refusal: []string{"REFUSE: hold"}}}

	require.NoError(t, rig.libWalk(t, rig.libCall("1 3", "")))

	assert.Equal(t, "report 3 1:REFUSE: hold  2  ", rig.libReport(t))
	assert.Len(t, rig.gate.ran(), 2, "the union, then 3 alone")
}

func TestBatchLibrary_ABaseThatMovesAfterTheGateStopsTheBatchAndDefersEveryMember(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	moved := rig.branchOn(t, "other", map[string]string{"other.txt": "other\n"})
	rig.h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Gate {
			rig.repo.must(t, "update-ref", "refs/heads/main", moved)
		}

		return nil
	}

	require.NoError(t, rig.libWalk(t, rig.libCall("1 3", `gate_run = ["check"], `)))

	assert.Equal(t, "report   1:"+reasonMoved+",3:"+reasonMoved+" 1  "+noLandingNote, rig.libReport(t))
	assert.Empty(t, rig.merger.landed(), "nothing merges on a tip that the gate did not see")
}

func TestBatchLibrary_AProgramGateReplacesTheProjectGate(t *testing.T) {
	t.Parallel()

	rig := libRig(t)

	require.NoError(t, rig.libWalk(t, rig.libCall("1 3", `gate_run = ["check"], `)))

	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
	assert.Empty(t, rig.gate.ran(), "the project gate does not run")
	assert.Contains(t, rig.h.commands.ran(), "check", "the program runs as the gate")
}

func TestBatchLibrary_AVoidGateEndsTheCallAndBlamesNoMember(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	rig.gate.void = true

	err := rig.libWalk(t, rig.libCall("1 3", ""))

	require.ErrorContains(t, err, "the gate gave no result: the gate did not run: node is 20.1.0 and the pin is 22.0.0. No member is blamed")
	assert.Empty(t, rig.merger.landed())
}

func TestBatchLibrary_AGateOnAnotherTreeThanTheUnionEndsTheCall(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	rig.gate.lie = emptyTree

	err := rig.libWalk(t, rig.libCall("1 3", ""))

	require.ErrorContains(t, err, "the gate ran on tree "+emptyTree)
	assert.Empty(t, rig.merger.landed())
}

func TestBatchLibrary_AGitCommandThatFailsEndsTheCallWithItsOutput(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	rig.h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{RC: 2, Stderr: "broken\n"}, nil
	}

	err := rig.libWalk(t, rig.libCall("1", ""))

	require.ErrorContains(t, err, "batch b: git cat-file failed with exit code 2: broken")
}

func TestBatchLibrary_ALostGateJobGatesTheSameUnionAgainAndTwoLossesEndTheCall(t *testing.T) {
	t.Parallel()

	one, _ := lostBatchRig(t, 1)
	one.runGit()
	one.h.queue = &fakeQueue{issueStates: map[int]string{1: issueClosed, 3: issueClosed}}

	require.NoError(t, one.libWalk(t, one.lostLibBody()))
	assert.Equal(t, []string{"1", "3"}, one.merger.landed(), "the rerun passed, so the batch lands")
	assert.Equal(t, []string{"losses 1 1 1"}, filterLosses(one.h.commands.ran()), "one gate, one loss record and host_losses = 1")

	two, hosts := lostBatchRig(t, 2)
	hosts.proofs = []model.LostProof{{Kind: model.LostStopped}, {Kind: model.LostGone}}
	two.runGit()

	err := two.libWalk(t, two.lostLibBody())

	require.ErrorContains(t, err, "the gate gave no result: the host lost two gate jobs of this batch: the gate job job-1 was lost on gpu-b and the proof is stopped; the gate job job-2 was lost on gpu-b and the proof is gone")
	assert.Empty(t, two.merger.landed())
}

// lostLibBody starts the host gpu-b, lands items 1 and 3 there with the library, then reports the gate count and the loss fields of the result.
func (r *batchRig) lostLibBody() string {
	return `load("@beehave//batch.star", "batch_merge")

def main(item):
    host_up(name = "start", host = "gpu-b")
    out = batch_merge(prs = [struct(item = "1", head = "` + r.heads["1"] + `", closes = []), struct(item = "3", head = "` + r.heads["3"] + `", closes = [])], base = "main", repo = "o/r", name = "b")
    command(name = "losses", run = ["losses", str(out.gates), str(out.host_losses), str(len(out.host_loss))])
`
}

// filterLosses keeps the losses lines of the commands that ran.
func filterLosses(ran []string) []string {
	var out []string

	for _, line := range ran {
		if strings.HasPrefix(line, "losses ") {
			out = append(out, line)
		}
	}

	return out
}

func TestBatchLibrary_TheIssuesOfTheLandedItemsAreReadAfterTheBatch(t *testing.T) {
	t.Parallel()

	rig := libRig(t)
	q := &fakeQueue{issueStates: map[int]string{1: issueOpen, 3: issueClosed}, pullClosing: map[int][]model.IssueRef{70: {"#3", "#9"}}}
	rig.h.queue = q

	require.NoError(t, rig.libWalk(t, rig.libCall("1 3", `integration = 70, `)))

	assert.Equal(t, `report 1,3   1 issue #1 of item 1 is not closed after the batch landed (the forge says "OPEN")|pull request #70 closes issue #9 and no landed item names it `, rig.libReport(t))
	assert.Equal(t, []int{1, 3}, q.issueAsked)
	assert.Equal(t, []int{70}, q.closingAsked)
}

func TestBatchLibrary_TheMembersAreChecked(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		prs, name, want string
	}{
		"no_name":   {prs: `struct(item = "1", head = "h", closes = [])`, name: "", want: "batch_merge needs a `name` that is not empty"},
		"twice":     {prs: `struct(item = "1", head = "h", closes = []), struct(item = "1", head = "h", closes = [])`, name: "b", want: "the batch lists 1 twice"},
		"no_member": {prs: `struct(item = "", head = "", closes = [])`, name: "b", want: "a batch member needs an item and a head commit"},
	}

	for label, one := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			rig := libRig(t)
			text := `load("@beehave//batch.star", "batch_merge")

def main(item):
    batch_merge(prs = [` + one.prs + `], base = "main", repo = "o/r", name = "` + one.name + `")
`
			require.ErrorContains(t, rig.libWalk(t, text), one.want)
			assert.Empty(t, rig.h.commands.ran(), "a refused call runs no git")
		})
	}
}

// baseAuditText is the text that the issue audit of the engine batch gave at the base on the issue states of the test below, line for line.
// The base audit read issue 4 (CLOSED) and 9 (OPEN), skipped the id bb-1, and found the issues 8 and 11 that pull request 70 closes and no item names.
func baseAuditText() []string {
	return []string{
		`issue #9 of item 9 is not closed after the batch landed (the forge says "OPEN")`,
		`the id "bb-1" of a landed item is not a plain issue number, so its issue was not read`,
		"pull request #70 closes issue #8 and no landed item names it",
		"pull request #70 closes issue #11 and no landed item names it",
	}
}

func TestBatchLibrary_TheAuditFindingsEqualTheTextOfTheBaseAudit(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	q := &fakeQueue{issueStates: map[int]string{4: issueClosed, 9: issueOpen}, pullClosing: map[int][]model.IssueRef{70: {"#11", "#4", "#8", "other/repo#2"}}}
	h.queue = q

	body := `load("@beehave//batch.star", "issue_audit")

def main(item):
    out = issue_audit(landed = ["4", "9", "bb-1"], integration = 70, name = "a")
    for line in out.findings:
        command(name = "seen", run = ["seen", line])
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, body), modeOff())

	require.NoError(t, err)

	var seen []string
	for _, line := range h.commands.ran() {
		seen = append(seen, strings.TrimPrefix(line, "seen "))
	}

	assert.Equal(t, baseAuditText(), seen)
	assert.Equal(t, []int{4, 9}, q.issueAsked, "the id that is not a plain number is not read")
	assert.Equal(t, []int{70}, q.closingAsked)
}

func TestBatchLibrary_AnIdWithALeadingZeroOrNoDigitsIsNotAPlainIssueNumber(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	q := &fakeQueue{}
	h.queue = q

	body := `load("@beehave//batch.star", "issue_audit")

def main(item):
    out = issue_audit(landed = ["07", ""], name = "a")
    command(name = "seen", run = ["seen", str(len(out.findings))])
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, body), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"seen 2"}, h.commands.ran())
	assert.Empty(t, q.issueAsked)
}
