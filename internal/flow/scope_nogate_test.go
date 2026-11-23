package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgepkg "github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/preflight"
)

// scopeOffNote is the note that the status of a run shows when the project has no forge.
const scopeOffNote = "scope check off: the project has no forge"

func TestMerge_ARunWithNoGateFixesItsScopeAtTheMergeCallAndAsksTheOwnerAboutANewIssue(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	done := rig.runMergeAs(t, engine, "1", "1")

	question := questionOf(t, rig.h, "1")
	require.NoError(t, rig.h.client(t, testVersion).Reply("1", Reply{Text: "no"}))
	require.NoError(t, receive(t, done).err)

	assert.Contains(t, question, "closes #13 now")
	assert.Contains(t, question, "(then: #12)")
	assert.Empty(t, rig.merger.landed(), "the refused member is not merged")
}

func TestMerge_ARunWithNoGateReadsTheScopeOnceAtTheMergeCall(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow(rig.call("1", "")+strings.Replace(rig.call("1", ""), `name = "b"`, `name = "c"`, 1))), modeOff())

	require.NoError(t, err)
	assert.Equal(t, 3, rig.h.forge.closingReads, "one read fixes the scope at the first merge call, and each of the two calls reads once to check its member")
}

func TestPreflight_ARunWithNoGateFixesItsScopeAtTheFirstReviewReadAndFixesItOnce(t *testing.T) {
	t.Parallel()

	h, _, _ := preflightHarness(t, preflight.Result{})
	h.forge.closing = [][]int{{12}, {12, 13}}

	_, err := walkBody(t, h, "preflight(name = \"a\", hold = \"hold\")\npreflight(name = \"b\", hold = \"hold\")\n")

	require.NoError(t, err)
	assert.Equal(t, 1, h.forge.closingReads, "the first review read fixes the scope and the second reads nothing")
}

func TestMerge_ARunWhoseScopeReadFailsAtTheMergeCallLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closingErr = errFake

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow(rig.call("1", ""))), modeOff())

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, `merge "b": scope of item 1`)
	assert.Empty(t, rig.merger.landed())
}

func TestPreflight_AScopeReadThatFailsEndsTheItemBeforeTheReviewRead(t *testing.T) {
	t.Parallel()

	h, fake, _ := preflightHarness(t, preflight.Result{})
	h.forge.closingErr = errFake

	_, err := walkBody(t, h, "preflight(name = \"a\", hold = \"hold\")\n")

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, fake.asked, "the scope is fixed before the review read, so a failed read of it reads no review")
}

func TestPreflight_AReadOfAnotherItemFixesNoScopeOfTheRunItem(t *testing.T) {
	t.Parallel()

	h, _, _ := preflightHarness(t, preflight.Result{})

	_, err := walkBody(t, h, "preflight(name = \"a\", id = \"7\", hold = \"hold\")\n")

	require.NoError(t, err)
	assert.Zero(t, h.forge.closingReads, "the pull request of another item is no first review of the run item")
}

func TestGate_AProjectWithNoForgeShowsInTheRunStatusThatTheScopeCheckIsOff(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closingErr = forgepkg.ErrNoForge
	engine := rig.engine(t)

	_, err := engine.RunFile("1", testProject, writeFlow(t, bodyFlow("gate(name = \"g\")\ngate(name = \"h\")\n")), modeOff())
	require.NoError(t, err)

	infos, err := rig.h.client(t, testVersion).Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Contains(t, infos[0].Notes(), scopeOffNote)
	assert.Equal(t, 1, rig.h.forge.closingReads, "the note is recorded once, and the second gate reads nothing")

	steps, err := rig.h.client(t, testVersion).Steps(infos[0].ID)
	require.NoError(t, err)
	assert.Contains(t, steps.Notes(), scopeOffNote)
}

func TestGate_AProjectWithAForgeShowsNoScopeOffNote(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow("gate(name = \"g\")\n")), modeOff())
	require.NoError(t, err)

	infos, err := rig.h.client(t, testVersion).Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.NotContains(t, infos[0].Notes(), scopeOffNote)
}

func TestRunProgress_AScopeThatIsOffIsOneNote(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{scopeOffNote}, RunProgress{ScopeOff: true}.Notes())
	assert.Empty(t, RunProgress{}.Notes())
}
