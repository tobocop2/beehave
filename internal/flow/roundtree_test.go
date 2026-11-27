package flow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const noWorkFlow = "def main(item):\n    pass\n"

func TestRoundTree_ARoundItemMakesItsWorktreeOnceBeforeItsWorkflowFileRuns(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, "def main(item):\n    command(name = \"probe\", run = [\"true\"])\n")

	tree, _ := gitTree(t)
	made := &ensureLog{}
	harness.trees = &fakeTrees{dir: tree, ensured: made}
	harness.commands = &fakeCommands{before: func(context.Context, CommandRequest) error {
		assert.Equal(t, []string{testRound}, made.all(), "the recorded step made the tree when the first command runs")

		return nil
	}}

	_, err := harness.engine(t).Run(testRound, testProject)

	require.NoError(t, err)
	assert.Equal(t, []string{testRound}, made.all(), "the recorded step made the tree once, and the workflow file asks for no more")
	assert.Equal(t, []string{"true"}, harness.commands.ran(), "the command step ran, so the check above ran")
}

func TestRoundTree_TheAgentStepOfARoundItemAsksForTheWorktreeThatTheRecordedStepMade(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	file := writeFlow(t, "def main(item):\n    command(name = \"first\", run = [\"true\"])\n    agent(name = \"a\", prompt = \"prompts/a.md\", model = \"m\")\n    command(name = \"last\", run = [\"true\"])\n")
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("Probe.\n"), 0o600))

	tree, _ := gitTree(t)
	made := &ensureLog{}
	harness.trees = &fakeTrees{dir: tree, ensured: made}

	_, err := harness.engine(t).RunFile(testRound, testProject, file, modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{testRound, testRound}, made.all(), "the recorded step and the agent step asked for the tree, and nothing else did")
	assert.Equal(t, []string{"true", "true"}, harness.commands.ran(), "both command steps ran")
}

func TestRoundTree_AResumedRunAsksForNoWorktreeBeforeItsSteps(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.life.failing["release"] = true

	tree, _ := gitTree(t)
	made := &ensureLog{}
	harness.trees = &fakeTrees{dir: tree, ensured: made}
	engine := harness.engine(t)

	_, err := engine.RunFile(testRound, testProject, writeFlow(t, "def main(item):\n    command(name = \"probe\", run = [\"true\"])\n"), modeOff())
	require.ErrorContains(t, err, "release")
	require.Equal(t, []string{testRound}, made.all(), "the first run made the tree once")

	harness.life.mu.Lock()
	harness.life.failing["release"] = false
	harness.life.mu.Unlock()

	_, err = resultOfHandle(t, resumeRunOf(t, engine, harness.client(t, testVersion), testRound))

	require.NoError(t, err)
	assert.Equal(t, []string{testRound}, made.all(), "the resume replayed the recorded step and asked for the tree no more")
}

func TestRoundTree_ARoundItemWhoseWorktreeCannotBeMadeEndsBeforeItsWorkflowFileRuns(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, "def main(item):\n    command(name = \"probe\", run = [\"true\"])\n")
	harness.trees = &fakeTrees{ensureErr: errFake}

	_, err := harness.engine(t).Run(testRound, testProject)

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "make worktree of item "+testRound)
	assert.Empty(t, harness.commands.ran(), "no command ran")
}

func TestRoundTree_AnItemThatIsNotARoundMakesNoWorktreeBeforeItsFirstStep(t *testing.T) {
	t.Parallel()

	for _, item := range []string{testItem, "bb-1"} {
		harness := newHarness(t)
		harness.flowIs(t, noWorkFlow)

		made := &ensureLog{}
		harness.trees = &fakeTrees{dir: t.TempDir(), ensured: made}

		_, err := harness.engine(t).Run(item, testProject)

		require.NoError(t, err)
		assert.Empty(t, made.all(), "item %s", item)
	}
}

func TestRoundTree_TheLostDiffOfARoundItemShowsTheWorkOfTheAgent(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	starts := &ensureLog{}
	harness.trees.starts = starts
	start := commitOf(t, harness.trees.dir)

	loseAgentOn(t, harness, testRound, 33*time.Minute)
	commitIn(t, harness.trees.dir, "done.txt")

	_, err := harness.engine(t).Run(testRound, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Contains(t, requests[1].Prompt, "done.txt", "the commit of the lost agent is in the summary")

	// The recorded step asks with no start. Every later call, the lost diff of the phase among them, asks at the commit that the step read.
	asked := starts.all()
	require.Len(t, asked, 4, "the make step, the first agent call, and the lost diff and the second agent call each asked")
	assert.Empty(t, asked[0])

	for i, got := range asked[1:] {
		assert.Equal(t, start, got, "call %d", i+1)
	}
}

// lostRig is a git folder with two commits, and an engine whose round worktree is that folder, or no folder when dir is empty.
type lostRig struct {
	dir, older, newer string
	made              *ensureLog
	engine            *Engine
}

func newLostRig(t *testing.T) lostRig {
	t.Helper()

	dir, older := gitTree(t)
	commitIn(t, dir, "earlier.txt")
	made := &ensureLog{}

	return lostRig{dir: dir, older: older, newer: commitOf(t, dir), made: made, engine: &Engine{deps: Deps{Trees: fakeTrees{dir: dir, starts: made}}}}
}

func TestRoundTree_TheLostDiffOfARoundItemListsTheChangesSinceTheStartOfTheCall(t *testing.T) {
	t.Parallel()

	rig := newLostRig(t)
	gitIn(t, rig.dir, "update-ref", callStartRef, rig.newer)
	require.NoError(t, os.WriteFile(filepath.Join(rig.dir, "earlier.txt"), []byte("agent"), 0o600))

	stat, view, err := rig.engine.lostDiff(t.Context(), PhaseInput{Item: testRound, StartHead: rig.newer, TreeStart: rig.older})

	require.NoError(t, err)
	assert.Equal(t, viewDiff, view)
	assert.Contains(t, stat, "earlier.txt", "the change of the agent since the start of the call")
	assert.Equal(t, []string{rig.older}, rig.made.all(), "the lost diff asks for the worktree at its tree start, so a removed one is made again there")
}

func TestRoundTree_TheLostDiffOfAWorktreeThatHoldsTheStartMarkListsTheChangesWhateverTheHistory(t *testing.T) {
	t.Parallel()

	rig := newLostRig(t)
	gitIn(t, rig.dir, "update-ref", callStartRef, rig.newer)
	gitIn(t, rig.dir, "reset", "-q", "--hard", rig.older)

	stat, view, err := rig.engine.lostDiff(t.Context(), PhaseInput{Item: testRound, StartHead: rig.newer})

	require.NoError(t, err)
	assert.Equal(t, viewDiff, view)
	assert.Contains(t, stat, "earlier.txt", "the agent reset below the start commit, so the diff shows the file of the earlier step deleted")
}

func TestRoundTree_TheLostDiffOfAWorktreeWithoutTheStartMarkAtTheStartCommitClaimsNothing(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		mark func(rig lostRig)
	}{
		{name: "made_again_with_no_mark", mark: func(lostRig) {}},
		{name: "mark_at_another_commit", mark: func(rig lostRig) { gitIn(t, rig.dir, "update-ref", callStartRef, rig.older) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rig := newLostRig(t)
			tc.mark(rig)
			require.NoError(t, os.WriteFile(filepath.Join(rig.dir, "earlier.txt"), []byte("agent"), 0o600))

			stat, view, err := rig.engine.lostDiff(t.Context(), PhaseInput{Item: testRound, StartHead: rig.newer})

			require.NoError(t, err)
			assert.Equal(t, viewOtherTree, view)
			assert.Empty(t, stat, "the folder is not the one that the call started in, so its diff says nothing about the call")
		})
	}
}

func TestRoundTree_TheLostDiffOfARoundItemWhoseWorktreeCannotBeMadeIsAnErrorThatNamesTheItem(t *testing.T) {
	t.Parallel()

	engine := &Engine{deps: Deps{Trees: fakeTrees{ensureErr: errFake}}}

	_, view, err := engine.lostDiff(t.Context(), PhaseInput{Item: testRound, StartHead: "abc123", TreeStart: "abc123"})

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "find the folder of item "+testRound)
	assert.Equal(t, viewUnread, view)
}

func TestRoundTree_TheLostDiffOfARoundItemWithNoFolderClaimsNothing(t *testing.T) {
	t.Parallel()

	engine := &Engine{deps: Deps{Trees: fakeTrees{}}}

	stat, view, err := engine.lostDiff(t.Context(), PhaseInput{Item: testRound, StartHead: "0000000000000000000000000000000000000000"})

	require.NoError(t, err)
	assert.Equal(t, viewOtherTree, view)
	assert.Empty(t, stat)
}

// commitOf returns the commit at HEAD of the git folder dir.
func commitOf(t *testing.T, dir string) string {
	t.Helper()

	head, err := folderHead(t.Context(), engineGit(dir))
	require.NoError(t, err)

	return head
}

func TestRoundTree_EveryLaterStepAsksForTheWorktreeAtTheCommitThatTheRecordedStepRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	file := writeFlow(t, "def main(item):\n    command(name = \"first\", run = [\"true\"])\n    gate(name = \"g\")\n    agent(name = \"a\", prompt = \"prompts/a.md\", model = \"m\")\n")
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("Probe.\n"), 0o600))

	tree, head := gitTree(t)
	starts := &ensureLog{}
	harness.trees = &fakeTrees{dir: tree, starts: starts}

	_, err := harness.engine(t).RunFile(testRound, testProject, file, modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"", head, head, head}, starts.all(), "the recorded step made the tree at the tip; the two steps of the gate and the agent ask for it at the commit it read")

	harness.commands.mu.Lock()
	defer harness.commands.mu.Unlock()

	require.Len(t, harness.commands.requests, 1)
	assert.Equal(t, head, harness.commands.requests[0].TreeStart, "the command carries the commit, so the runner makes a removed tree again there")
}

func TestRoundTree_ARoundItemOfAProjectWithNoRepositoryMakesNoWorktreeAndItsCommandsCarryNoTreeStart(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, "def main(item):\n    command(name = \"probe\", run = [\"true\"])\n")
	harness.repository = func(string) bool { return false }

	made := &ensureLog{}
	harness.trees = &fakeTrees{dir: t.TempDir(), ensured: made}

	_, err := harness.engine(t).Run(testRound, testProject)

	require.NoError(t, err)
	assert.Empty(t, made.all(), "no step made a tree")

	harness.commands.mu.Lock()
	defer harness.commands.mu.Unlock()

	require.Len(t, harness.commands.requests, 1)
	assert.Empty(t, harness.commands.requests[0].TreeStart, "the command runs in an empty folder that beehest removes, as at the base")
}

func TestRoundTree_TheProjectGateMakesARemovedWorktreeAgainAtItsTreeStartAndARequestWithNoneMakesNothing(t *testing.T) {
	t.Parallel()

	made := &ensureLog{}
	engine := &Engine{deps: Deps{Trees: fakeTrees{ensured: made, ensureErr: errFake}}}

	require.NoError(t, engine.remakeTree(t.Context(), CommandRequest{Item: "7"}))
	assert.Empty(t, made.all(), "a request with no tree start asks for no tree")

	err := engine.remakeTree(t.Context(), CommandRequest{Item: testRound, TreeStart: "abc123"})

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "find the folder of item "+testRound)
	assert.Equal(t, []string{testRound}, made.all())
}

func TestRoundTree_AProjectGateWhoseRemovedWorktreeCannotBeMadeAgainStopsBeforeTheGateReadsTheFolder(t *testing.T) {
	t.Parallel()

	gate := &fakeGate{}
	engine := &Engine{deps: Deps{Trees: fakeTrees{ensureErr: errFake}, Gate: gate}}
	request := CommandRequest{Item: testRound, TreeStart: "abc123"}

	start, err := engine.reuseProjectGate(t.Context(), request)

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "find the folder of item "+testRound)
	assert.Equal(t, gateStart{}, start)
	assert.Zero(t, gate.keyReads, "the key of the gate is not read in a folder that is missing")

	finished, key, err := engine.finishProjectGate(t.Context(), ExclusiveInput{Request: request}, gateStart{Key: GateKey{ID: "k"}})

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "find the folder of item "+testRound)
	assert.Equal(t, gateFinish{}, finished)
	assert.Empty(t, key)
	assert.Empty(t, gate.jobs, "the gate does not run in a folder that is missing")
}

const childAgentFlow = "def worker(item):\n    agent(name = \"a\", prompt = \"prompts/a.md\", model = \"m\")\n\ndef main(item):\n    gather(spawn(items = [\"21\"], entry = \"worker\", name = \"fan\"), name = \"fan\")\n"

func TestRoundTree_AChildItemOfARoundMakesItsWorktreeAtTheBaseOfTheRound(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	made, bases := &ensureLog{}, &ensureLog{}
	rig.h.trees = &fakeTrees{dir: rig.dir, ensured: made, bases: bases}

	_, err := rig.h.engine(t).RunFile(testRound, testProject, flowFile(t, childAgentFlow, "Probe.\n"), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{testRound, "21"}, made.all(), "the round made its own tree, then the child made its tree")
	assert.Equal(t, []string{"", "integration/s1"}, bases.all(), "the round item names no base, and its child names the base of the round")
}

func TestRoundTree_AChildItemOfARunThatIsNoRoundMakesItsWorktreeAtTheBaseOfTheProject(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	made, bases := &ensureLog{}, &ensureLog{}
	rig.h.trees = &fakeTrees{dir: rig.dir, ensured: made, bases: bases}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, flowFile(t, childAgentFlow, "Probe.\n"), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21"}, made.all(), "only the child made a tree")
	assert.Equal(t, []string{""}, bases.all(), "a parent with no round gives the child no base")
}

func TestRoundTree_ThePhaseOfAChildItemCarriesTheBaseOfItsRound(t *testing.T) {
	t.Parallel()

	got := agentArgs{}.phase(ItemInput{Item: "21", Project: testProject, Base: "integration/s1"}, "login", "prompt", "")

	assert.Equal(t, "integration/s1", got.Base)
}

func TestRoundTree_TheLostFolderOfAChildItemIsMadeAtTheBaseOfItsRound(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	bases := &ensureLog{}
	tree, _ := gitTree(t)
	harness.trees = &fakeTrees{dir: tree, bases: bases}

	_, _, err := harness.engine(t).lostFolder(t.Context(), PhaseInput{Item: "21", Project: testProject, Base: "integration/s1"})

	require.NoError(t, err)
	assert.Equal(t, []string{"integration/s1"}, bases.all())
}

// childFlowOnBase is childAgentFlow with the round base that the settings name passed to spawn.
func childFlowOnBase(base string) string {
	return "def worker(item):\n    agent(name = \"a\", prompt = \"prompts/a.md\", model = \"m\")\n\ndef main(item):\n    gather(spawn(items = [\"21\"], entry = \"worker\", base = \"" + base + "\", name = \"fan\"), name = \"fan\")\n"
}

func TestRoundTree_ASpawnWhoseBaseIsTheBaseOfTheRoundItemStartsItsChild(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	made, bases := &ensureLog{}, &ensureLog{}
	rig.h.trees = &fakeTrees{dir: rig.dir, ensured: made, bases: bases}

	_, err := rig.h.engine(t).RunFile(testRound, testProject, flowFile(t, childFlowOnBase("integration/s1"), "Probe.\n"), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"", "integration/s1"}, bases.all())
}

func TestRoundTree_ASpawnWhoseBaseDiffersFromTheBaseOfTheRoundItemStartsNoChildAndNamesBothBases(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	made := &ensureLog{}
	rig.h.trees = &fakeTrees{dir: rig.dir, ensured: made}

	_, err := rig.h.engine(t).RunFile(testRound, testProject, flowFile(t, childFlowOnBase("integration/s2"), "Probe.\n"), modeOff())

	require.ErrorContains(t, err, "the round item "+testRound+" has the base integration/s1, and the base given to spawn is integration/s2")
	assert.Equal(t, []string{testRound}, made.all(), "no child made a tree")
}

func TestRoundTree_ASpawnThatNamesABaseUnderAnItemThatIsNoRoundCutsItsChildAtThatBase(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	made, bases := &ensureLog{}, &ensureLog{}
	rig.h.trees = &fakeTrees{dir: rig.dir, ensured: made, bases: bases}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, flowFile(t, childFlowOnBase("integration/s1"), "Probe.\n"), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"integration/s1"}, bases.all(), "the child is cut at the base that its gate reads, though no round item named it")
}
