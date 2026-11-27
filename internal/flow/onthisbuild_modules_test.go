package flow

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gateWaitFlow records a gate step, then waits for the owner.
const gateWaitFlow = "def main(item):\n    gate(name = \"g\")\n    wait(name = \"ask\", question = \"What now?\")\n"

// factsWaitFlow records a facts step, then waits for the owner.
const factsWaitFlow = "def main(item):\n    facts()\n    wait(name = \"ask\", question = \"What now?\")\n"

// buildWithModule is a client of another build whose module is at another version.
func buildWithModule(c *Client, module string, version int) *Client {
	c.version = versionWithModule(otherBuild, module, version)

	return c
}

// buildAtVersionOne is a client of another build whose core, agent and forge modules are at version 1: every waiting run of the harness recorded a step of
// the core and the agent module, and the forge module covers the step that reads who wrote the item in every run.
func buildAtVersionOne(c *Client) *Client {
	table := moduleTable()
	parts := make([]string, len(table))

	for i, entry := range table {
		number := entry.version
		if entry.name == moduleCore || entry.name == moduleAgent || entry.name == moduleForge {
			number = 1
		}

		parts[i] = entry.name + strconv.Itoa(number)
	}

	c.version = otherBuild + "+" + strings.Join(parts, ".")

	return c
}

func TestClient_ResumeOnThisBuildMovesARunThatRecordedNoStepOfTheModuleThatChanged(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	buildWithModule(other, moduleGate, gateVersion+1)
	expireLease(harness)

	require.NoError(t, other.ResumeOnThisBuild(testItem), "the run recorded agent steps and no gate step")

	row, err := other.row(testRun)
	require.NoError(t, err)
	assert.Equal(t, other.version, row.ApplicationVersion)
}

func TestClient_ResumeOnThisBuildRefusesARunThatRecordedAStepOfTheModuleThatChanged(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	buildWithModule(other, moduleAgent, agentVersion+1)
	expireLease(harness)

	err := other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, fmt.Sprintf("agent module at version %d and this build has version %d", agentVersion, agentVersion+1))
	require.ErrorContains(t, err, "Start the item again")
	assert.NotContains(t, err.Error(), "End the failed run first", "an open run is not failed, so no cancel is needed")

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "a refused run stays where it was")
}

func TestClient_ResumeOnThisBuildRefusesARunRecordedUnderAgentModule5(t *testing.T) {
	t.Parallel()

	// Agent module 5 looked at the caps in the item run, before the phase. This build looks inside the phase, so the old record has another shape.
	harness, _, other := strandedHarness(t)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?",
		versionWithModule(testVersion, moduleAgent, 5), testRun)
	require.NoError(t, err)
	expireLease(harness)

	err = other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, fmt.Sprintf("agent module at version 5 and this build has version %d", agentVersion))
}

func TestClient_ResumeOnThisBuildRefusesARunRecordedUnderCoreModule3(t *testing.T) {
	t.Parallel()

	// Core module 3 recorded no wait:notify step. A replay of its wait under this build expects that step where the record holds the receive.
	harness, _, other := strandedHarness(t)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?",
		versionWithModule(testVersion, moduleCore, 3), testRun)
	require.NoError(t, err)
	expireLease(harness)

	err = other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, fmt.Sprintf("core module at version 3 and this build has version %d", coreVersion))
}

func TestClient_ResumeOnThisBuildRefusesARunRecordedUnderCoreModule7(t *testing.T) {
	t.Parallel()

	// Core module 7 recorded the result of command and write with no attempts field. The run stays with the build that recorded it.
	harness, _, other := strandedHarness(t)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?",
		versionWithModule(testVersion, moduleCore, 7), testRun)
	require.NoError(t, err)
	expireLease(harness)

	err = other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, fmt.Sprintf("core module at version 7 and this build has version %d", coreVersion))
}

func TestClient_ResumeOnThisBuildRefusesARunThatRecordedAGateStepWhenTheGateModuleChanged(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, gateWaitFlow)
	harness.gate.results = []GateResult{{Passed: true, Head: gatedHead}}

	runInBackground(harness.engine(t), testItem)
	waitOn(t, ownerAsks(t, harness))
	expireLease(harness)

	other := buildWithModule(harness.client(t, otherBuild), moduleGate, gateVersion+1)

	err := other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, "gate module")
}

func TestClient_ResumeOnThisBuildMovesARunStoredUnderTheSingleVersionOfTheSteps(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion+"+"+stepsVersion, testRun)
	require.NoError(t, err)

	buildAtVersionOne(other)
	expireLease(harness)

	require.NoError(t, other.ResumeOnThisBuild(testItem), "the single version stands for every module at version 1, and this build has the core module (wait:notify) and the agent module at version 1")

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, other.version, row.ApplicationVersion)
}

func TestClient_ResumeOnThisBuildRefusesARunStoredUnderTheSingleVersionOfTheStepsWhenTheCoreModuleChanged(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, factsWaitFlow)

	runInBackground(harness.engine(t), testItem)
	waitOn(t, ownerAsks(t, harness))

	other := harness.client(t, otherBuild)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion+"+"+stepsVersion, testRun)
	require.NoError(t, err)

	expireLease(harness)

	require.ErrorContains(t, other.ResumeOnThisBuild(testItem), fmt.Sprintf("core module at version 1 and this build has version %d", coreVersion),
		"the facts result of the old build has another list of fields than the core module of this build")
}

func TestClient_ResumeOnThisBuildRefusesARunStoredUnderTheSingleVersionWhenAUsedModuleChanged(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion+"+"+stepsVersion, testRun)
	require.NoError(t, err)

	other.version = versionWithModule(otherBuild, moduleCore, 1) // the core module matches, so the agent module is the first that differs
	expireLease(harness)

	require.ErrorContains(t, other.ResumeOnThisBuild(testItem), fmt.Sprintf("agent module at version 1 and this build has version %d", agentVersion))
}

func TestClient_ResumeOnThisBuildRefusesARunStoredUnderAMalformedOrRepeatingList(t *testing.T) {
	t.Parallel()

	for tail, want := range map[string]string{
		"core1.gate":  `entry "gate" of the step list "core1.gate"`,
		"core1.core2": `names the module core twice`,
	} {
		harness, _, other := strandedHarness(t)
		_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion+"+"+tail, testRun)
		require.NoError(t, err)

		expireLease(harness)

		err = other.ResumeOnThisBuild(testItem)
		require.ErrorIs(t, err, errStranded)
		require.ErrorContains(t, err, want)
	}
}

func TestClient_AStepListThatTheStoreCannotReadRefusesTheMoveWithTheWordsOfTheRead(t *testing.T) {
	t.Parallel()

	_, _, other := strandedHarness(t)
	last, err := other.newest(testItem)
	require.NoError(t, err)
	require.NoError(t, dbos.Shutdown(other.flow, shutdownTimeout))

	err = other.requireUsedModulesSame(testItem, last, map[string]int{moduleGate: 1}, map[string]int{moduleGate: 2})

	require.ErrorContains(t, err, "read the steps of "+last.Status.ID)
	require.NotErrorIs(t, err, errStranded, "a read that failed says nothing about the step list")
}

func TestClient_ResumeOnThisBuildMovesARunStoredUnderAListOfSomeModules(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion+"+core"+strconv.Itoa(coreVersion)+".agent"+strconv.Itoa(agentVersion)+".forge"+strconv.Itoa(forgeVersion), testRun)
	require.NoError(t, err)

	buildWithModule(other, moduleGate, gateVersion+1)
	expireLease(harness)

	require.NoError(t, other.ResumeOnThisBuild(testItem), "the stored list names the modules of the run, and the run used neither the missing ones nor the gate")
}

func TestClient_StepNamesReportsARunThatCannotBeRead(t *testing.T) {
	t.Parallel()

	closed, err := OpenClient(t.Context(), newHarness(t).opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	_, err = closed.stepNames(testRun)

	require.ErrorContains(t, err, "read the steps of "+testRun)
}

// olderRoundRun leaves an open run of item that a build with the module at version recorded, and returns a client of this build.
// The project has no repository, so the run records no step that makes a worktree, as a run of the older build did not.
func olderRoundRun(t *testing.T, item, module string, version int) (*harness, *Client) {
	t.Helper()

	harness := newHarness(t)
	harness.repository = func(string) bool { return false }
	harness.flowIs(t, "def main(item):\n    wait(name = \"ask\", question = \"What now?\")\n")

	runInBackground(harness.engine(t), item)
	waitOn(t, ownerAsks(t, harness))

	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?",
		versionWithModule(testVersion, module, version), runID(item, 1))
	require.NoError(t, err)

	expireLease(harness)

	return harness, harness.client(t, otherBuild)
}

func TestClient_ResumeOnThisBuildRefusesARoundRunOfABuildWithAnOlderRoundModuleAndNamesTheStepThatMakesTheWorktree(t *testing.T) {
	t.Parallel()

	_, other := olderRoundRun(t, testRound, moduleRound, 1)

	err := other.ResumeOnThisBuild(testRound)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, fmt.Sprintf("round module at version 1, and this build has version %d", roundVersion))
	require.ErrorContains(t, err, `the step "make worktree"`)
	require.ErrorContains(t, err, "Finish the run on the build that started it, or start the item again: beehest run "+testRound)
	require.NotContains(t, err.Error(), "UnexpectedStep")

	row, rowErr := other.row(runID(testRound, 1))
	require.NoError(t, rowErr)
	assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "a refused run stays where it was")
}

func TestClient_ResumeOnThisBuildRefusesARunOfABuildWithAnOlderForgeModuleAndNamesTheStepThatReadsWhoWroteTheItem(t *testing.T) {
	t.Parallel()

	for _, item := range []string{testItem, testRound} {
		_, other := olderRoundRun(t, item, moduleForge, forgeVersion-1)

		err := other.ResumeOnThisBuild(item)

		require.ErrorIs(t, err, errStranded, item)
		require.ErrorContains(t, err, fmt.Sprintf("forge module at version %d, and this build has version %d", forgeVersion-1, forgeVersion), item)
		require.ErrorContains(t, err, `the step "origin"`, item)
		require.ErrorContains(t, err, "start the item again: beehest run "+item, item)
	}
}

func TestClient_ResumeOnThisBuildRefusesARunRecordedAtForgeTwelveWhichHasNoOriginStep(t *testing.T) {
	t.Parallel()

	// Forge 12 is the last version of the forge module whose runs record no origin step, so this build must not move such a run.
	const lastWithoutOrigin = 12

	h, other := olderRoundRun(t, testItem, moduleForge, lastWithoutOrigin)

	// The run of that build recorded no origin step.
	_, err := sqlOf(t, h).ExecContext(t.Context(), "DELETE FROM operation_outputs WHERE workflow_uuid = ? AND function_name = ?", runID(testItem, 1), originStepName)
	require.NoError(t, err)

	err = other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, fmt.Sprintf("forge module at version %d", lastWithoutOrigin))
	require.ErrorContains(t, err, `the step "origin"`)
}

func TestClient_ResumeOnThisBuildMovesARunOfAnItemThatIsNotARoundWhenOnlyTheRoundModuleChanged(t *testing.T) {
	t.Parallel()

	_, other := olderRoundRun(t, testItem, moduleRound, 1)

	require.NoError(t, other.ResumeOnThisBuild(testItem), "the run recorded no step of the round module, and it is not a round item")
}

func TestClient_ResumeOnThisBuildMovesARoundRunWhenOnlyAModuleThatTheRunDidNotUseChanged(t *testing.T) {
	t.Parallel()

	// The stored gate version must differ from this build, or the lists are equal and the round check never runs.
	_, other := olderRoundRun(t, testRound, moduleGate, gateVersion-1)

	require.NoError(t, other.ResumeOnThisBuild(testRound), "the round module is the same, and the run recorded no gate step")
}

// recordSteps adds steps of the given names to the record of the first run of an item, after the steps that it recorded.
func recordSteps(t *testing.T, h *harness, item string, names ...string) {
	t.Helper()

	for i, name := range names {
		_, err := sqlOf(t, h).ExecContext(t.Context(), "INSERT INTO operation_outputs (workflow_uuid, function_id, function_name) VALUES (?, ?, ?)",
			runID(item, 1), 9000+i, name)
		require.NoError(t, err)
	}
}

func TestClient_ResumeOnThisBuildRefusesARunThatRecordedAStepOfTheSingleMergeAndNamesTheStepAndItsModule(t *testing.T) {
	t.Parallel()

	// A run of the build before the one merge builtin holds the steps of the single merge, which this build does not record.
	tests := map[string]struct {
		land   int
		steps  []string
		module string
	}{
		"a scope check and a merge":                          {4, []string{"scope-check:m#1", "merge:m#1"}, moduleRound},
		"a scope check in the owner wait, under equal lists": {landVersion, []string{"scope-check:m#1", "wait:scope#1"}, moduleRound},
		"a before_review step":                               {gateVersion, []string{"before_review:candidate-before-review#1"}, moduleGate},
		"the read of the owner merge wait":                   {landVersion, []string{"watch", "DBOS.sleep", "watch"}, moduleCore},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness, other := olderRoundRun(t, testItem, moduleLand, test.land)
			recordSteps(t, harness, testItem, test.steps...)

			err := other.ResumeOnThisBuild(testItem)

			require.ErrorIs(t, err, errStranded)
			require.ErrorContains(t, err, fmt.Sprintf("the step %q, and an older build recorded it for the %s module, and this build does not record it", test.steps[0], test.module))
			require.ErrorContains(t, err, "Start the item again: beehest run "+testItem)

			row, rowErr := other.row(runID(testItem, 1))
			require.NoError(t, rowErr)
			assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "a refused run stays where it was")
		})
	}
}

func TestClient_ResumeOnThisBuildMovesARunThatRecordedTheStepsOfAMergeOfMembers(t *testing.T) {
	t.Parallel()

	harness, other := olderRoundRun(t, testItem, moduleLand, landVersion)
	recordSteps(t, harness, testItem, "merge:u.plan", "merge:u.intent#1", "merge:u.poll#1", "merge:u.tip")

	require.NoError(t, other.ResumeOnThisBuild(testItem), "this build records the steps of a merge of members")

	row, err := other.row(runID(testItem, 1))
	require.NoError(t, err)
	assert.Equal(t, other.version, row.ApplicationVersion, "the run is stored under this build")
}

func TestClient_ResumeOnThisBuildRefusesARunWithTheSingleMergeOfAnOlderLandModuleByItsVersion(t *testing.T) {
	t.Parallel()

	// Land module 4 recorded the single merge as merge:<name>#<n>. The kind merge is live again, so the version of the land module refuses the run.
	harness, other := olderRoundRun(t, testItem, moduleLand, 4)
	recordSteps(t, harness, testItem, "merge:m#1")

	err := other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, fmt.Sprintf("recorded steps of the land module at version 4 and this build has version %d", landVersion))
}

func TestClient_ResumeOnThisBuildRefusesARunThatRecordedARetiredAssignStepAndNamesItsModule(t *testing.T) {
	t.Parallel()

	// Tracker module 1 recorded item_assign, forge module 2 recorded pr_assign and forge module 3 recorded board_sync. This build retired all three.
	tests := map[string]struct {
		module, step string
		version      int
	}{
		"item_assign": {moduleTracker, "item_assign:a#1", 1},
		"pr_assign":   {moduleForge, "pr_assign:a#1", 2},
		"board_sync":  {moduleForge, "board_sync:a#1", 3},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness, other := olderRoundRun(t, testItem, test.module, test.version)
			recordSteps(t, harness, testItem, test.step)

			err := other.ResumeOnThisBuild(testItem)

			require.ErrorIs(t, err, errStranded)
			require.ErrorContains(t, err, fmt.Sprintf("the step %q, and an older build recorded it for the %s module, and this build does not record it", test.step, test.module))
		})
	}
}

func TestClient_ResumeOnThisBuildRefusesARunThatRecordedTheRetiredQueueStepAndNamesTheRoundModule(t *testing.T) {
	t.Parallel()

	// Round module 5 recorded the step queue. The queue is a library function now, and this build retired the step.
	harness, other := olderRoundRun(t, testItem, moduleRound, 5)
	recordSteps(t, harness, testItem, "queue:q#1")

	err := other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, fmt.Sprintf("the step %q, and an older build recorded it for the %s module, and this build does not record it", "queue:q#1", moduleRound))
}

func TestClient_ResumeOnThisBuildRefusesARunThatRecordedAStepOfNoModuleAndNamesTheStep(t *testing.T) {
	t.Parallel()

	for name, version := range map[string]int{"the lists differ": 4, "the lists are equal": landVersion} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness, other := olderRoundRun(t, testItem, moduleLand, version)
			recordSteps(t, harness, testItem, "unknown_kind:u#1")

			err := other.ResumeOnThisBuild(testItem)

			require.ErrorIs(t, err, errStranded)
			require.ErrorContains(t, err, `the step "unknown_kind:u#1", and no module of this build records that step`)
			require.ErrorContains(t, err, "Start the item again: beehest run "+testItem)

			row, rowErr := other.row(runID(testItem, 1))
			require.NoError(t, rowErr)
			assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "a refused run stays where it was")
		})
	}
}

func TestClient_ResumeOnThisBuildMovesARunWhoseStepsEveryModuleOrTheEngineClaims(t *testing.T) {
	t.Parallel()

	harness, other := olderRoundRun(t, testItem, moduleLand, 4)
	recordSteps(t, harness, testItem, "hold:start", "undo:write:w#1", "worktree", "others", "phase", "exclusive", "host_job", "DBOS.sleep")

	require.NoError(t, other.ResumeOnThisBuild(testItem), "the run recorded no land step, and every step has a module or is a step of the engine")
}

func TestClient_ResumeOnThisBuildRefusesARunThatRecordedAGateStepUnderGateModule5(t *testing.T) {
	t.Parallel()

	// Gate module 5 could record the step before_review, which this build retired. A run that recorded a gate step under it does not move.
	harness := newHarness(t)
	harness.flowIs(t, gateWaitFlow)
	harness.gate.results = []GateResult{{Passed: true, Head: gatedHead}}

	runInBackground(harness.engine(t), testItem)
	waitOn(t, ownerAsks(t, harness))

	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?",
		versionWithModule(testVersion, moduleGate, 5), testRun)
	require.NoError(t, err)
	expireLease(harness)

	other := harness.client(t, otherBuild)

	err = other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, fmt.Sprintf("gate module at version 5 and this build has version %d", gateVersion))
}
