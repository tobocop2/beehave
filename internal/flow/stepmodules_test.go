package flow

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// versionWithModule is the application version of a build whose module is at another version than the table says.
func versionWithModule(build, module string, version int) string {
	table := moduleTable()
	parts := make([]string, len(table))

	for i, entry := range table {
		number := entry.version
		if entry.name == module {
			number = version
		}

		parts[i] = entry.name + strconv.Itoa(number)
	}

	return build + "+" + strings.Join(parts, ".")
}

func TestStepModuleTable_EveryStepKindOfTheModuleBelongsToExactlyOneModule(t *testing.T) {
	t.Parallel()

	owners := map[string][]string{}

	for _, module := range moduleTable() {
		for _, kind := range slices.Concat(module.kinds, module.engine, module.retired) {
			owners[kind] = append(owners[kind], module.name)
		}
	}

	for kind, modules := range owners {
		assert.Len(t, modules, 1, "the step kind %q is in the modules %v", kind, modules)
	}

	for _, kind := range StepModule().Kinds {
		assert.Contains(t, owners, kind, "the step kind %q has no module", kind)
	}
}

func TestStepModuleTable_NamesNoStepKindThatTheModuleDoesNotRecord(t *testing.T) {
	t.Parallel()

	recorded := StepModule().Kinds

	for _, module := range moduleTable() {
		for _, kind := range module.kinds {
			assert.True(t, kind == kindBatch || slices.Contains(recorded, kind), "the module %s names the kind %q, which no builtin records", module.name, kind)
		}
	}
}

func TestStepModuleTable_HoldsTheEightModulesOnceEach(t *testing.T) {
	t.Parallel()

	var names []string

	for _, module := range moduleTable() {
		names = append(names, module.name)
	}

	assert.Equal(t, []string{"core", "agent", "tracker", "forge", "land", "gate", "host", "round"}, names)
}

func TestApplicationVersion_HoldsOneVersionForEachModule(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "v1+core20.agent15.tracker4.forge11.land13.gate7.host1.round6", applicationVersion("v1"))
	assert.Equal(t, "v1+core20.agent15.tracker4.forge11.land13.gate7.host1.round6", versionWithModule("v1", moduleGate, 7))
}

func TestStepModules_TheCoreModuleIsAtVersionTwenty(t *testing.T) {
	t.Parallel()

	assert.Contains(t, strings.Split(moduleVersions(), "."), "core20", "the facts step records ten fields: the method and board fields moved under vars at core5 and runners came at core6, so a run recorded at core5 replays no facts step, "+
		"the host job of a command call with a slot class names its run at core7, the sleep step records its marker at core8, the publish and progress steps and the cost_usd of a gather outcome come at core9, "+
		"the recv and send steps of the inbox come at core10, the result of command and write records attempts at core11, "+
		"the cancel step and the deadline steps of a gather come at core12, the runners entry of the facts holds loads_skills in place of reports_skills at core13, "+
		"the first_of step with its look steps comes at core14, and gather has no until keyword, "+
		"the answers of wait arrive on the inbox at core15, and a plain wait with a timeout records its deadline step, "+
		"and the deadline step of a recv call records the topic with the deadline at core16, "+
		"and a send to an item records the run it names in the step send:<name>#<n>.to at core17, "+
		"and the stream step with its deadline step, which records the end of the wait and the child run, comes at core18, "+
		"and a host job with no slot class runs in the default class at core19, "+
		"and the earlier_reads step comes at core20")
}

func TestStepModules_TheLandModuleIsAtVersionThirteen(t *testing.T) {
	t.Parallel()

	assert.Contains(t, strings.Split(moduleVersions(), "."), "land13", "merge is the old batch_merge and a merge of one pull request records the batch steps, "+
		"each member records its scope check as batch:<call>.scope#<item> before its intent, a hand-off records batch:<call>.ready#<item> and batch:<call>.watch#<item>@<round>, "+
		"the preflight step records no foreign, pr, head or author field, the merge of a member names its run at land9, "+
		"an owner line that keeps issues records batch:<call>.kept#<item> at land10, "+
		"the scope records each issue as text, with the repository for an issue of another repository, at land11, "+
		"and the first review read and the first merge call of a run that no gate gave a scope record the scope step at land12, "+
		"and each member records the issues that its pull request must close, which the merge audits, at land13")
}

func TestStepModules_TheGateModuleIsAtVersionSeven(t *testing.T) {
	t.Parallel()

	assert.Contains(t, strings.Split(moduleVersions(), "."), "gate7", "the gate step records the step field, the name of its own step, so a run that recorded a gate before this version does not replay it, "+
		"records a lost job and host_losses, the wait for its proof records gate:lost-start and gate:lost-look.<k>, "+
		"the owner wait records <call>/lost-ask.<n>, the item run records a lost job with a proof as <call>/host-loss, gate records the revert field, and the kind before_review is retired")
}

func TestParseVersions_TheSingleVersionOfTheStepsIsEveryModuleAtVersionOne(t *testing.T) {
	t.Parallel()

	got, ok := parseVersions(stepsVersion)

	require.True(t, ok)
	assert.Len(t, got, len(moduleTable()))

	for _, module := range moduleTable() {
		assert.Equal(t, 1, got[module.name], module.name)
	}
}

func TestParseVersions_ReadsAListAndRefusesAnOlderSingleVersionAndNoise(t *testing.T) {
	t.Parallel()

	got, ok := parseVersions("core2.gate3")
	require.True(t, ok)
	assert.Equal(t, map[string]int{"core": 2, "gate": 3}, got)

	for _, text := range []string{"star12", "", "core", "core1..gate1", "core99999999999999999999"} {
		_, ok = parseVersions(text)
		assert.False(t, ok, "%q is not readable", text)
	}
}

func TestReadVersions_NamesTheBadEntryAndTheModuleThatRepeats(t *testing.T) {
	t.Parallel()

	_, err := readVersions("core1.gate")
	require.ErrorContains(t, err, `entry "gate" of the step list "core1.gate"`)

	_, err = readVersions("core1.gate2.core3")
	require.ErrorContains(t, err, "names the module core twice")
}

func TestSplitVersion_KeepsAMalformedListOfModuleVersionsAsTheStepList(t *testing.T) {
	t.Parallel()

	for _, tail := range []string{"core1.gate", "core1.gatex", "core1.core2"} {
		build, steps := splitVersion("v1+" + tail)
		assert.Equal(t, "v1", build)
		assert.Equal(t, tail, steps)
	}
}

func TestSplitVersion_ReadsTheListOfModuleVersionsAndKeepsABuildTagThatLooksLikeOne(t *testing.T) {
	t.Parallel()

	build, steps := splitVersion("v1+rc1+core1.gate2")
	assert.Equal(t, "v1+rc1", build)
	assert.Equal(t, "core1.gate2", steps)

	build, steps = splitVersion("v1+rc1")
	assert.Equal(t, "v1+rc1", build, "rc1 has the shape of a module version, but no module is named rc")
	assert.Empty(t, steps)

	build, steps = splitVersion("+core1.gate2")
	assert.Empty(t, build, "a plus sign at the start has an empty build version before it")
	assert.Equal(t, "core1.gate2", steps)

	build, steps = splitVersion("v1+star30")
	assert.Equal(t, "v1", build)
	assert.Equal(t, "star30", steps)
}

func TestModulesUsed_AreTheModulesOfTheKindsThatTheStepsStartWith(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"tracker"}, modulesUsed([]string{"claim", "item_read:own#1", "DBOS.sleep", "tree", "item_comment:c#2"}))
	assert.Equal(t, []string{"core", "agent", "land"}, modulesUsed([]string{"agent:a#1/render", "wait:w#1", "batch:b.issues", "now#1"}))
	assert.Equal(t, []string{"gate", "round"}, modulesUsed([]string{"gate:g#1", "scope:g#1", "gate:tip@2"}))
	assert.Empty(t, modulesUsed(nil))
}

func TestModulesUsed_TheStepsOfTheWaitsOfTheOwnerAreStepsOfTheCoreModule(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"core"}, modulesUsed([]string{"claim", "DBOS.recv"}), "the receive of an answer")
	assert.Equal(t, []string{"core"}, modulesUsed([]string{"claim", "watch", "DBOS.sleep", "watch"}), "the read of a ready pull request that an older build recorded")
	assert.Empty(t, modulesUsed([]string{"claim", "DBOS.sleep"}), "a sleep alone is the wait of a cap or a limit, not of the owner")
}

func TestModulesUsed_TheStepsThatTheEngineOrAnOlderBuildRecordsForAModuleAreStepsOfThatModule(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"core"}, modulesUsed([]string{"undo:write:w#1"}), "the undo of a write")
	assert.Equal(t, []string{"round"}, modulesUsed([]string{holdStep}), "the read of the hold at the start of a spawned run")
	assert.Equal(t, []string{"land", "round"}, modulesUsed([]string{"scope-check:m#1", "merge:m#1"}), "the scope check and the merge of the single merge of an older build")
}

func TestUnreplayableStep_IsTheFirstStepThatThisBuildDoesNotRecordWithTheModuleOfTheOlderBuild(t *testing.T) {
	t.Parallel()

	engine := []string{"item", "claim", "ready", "release", "worktree", "make worktree", "others", "phase", "exclusive", "host_job", "DBOS.sleep", "DBOS.getResult"}
	_, _, found := unreplayableStep(slices.Concat(engine, []string{"wait:w#1", "agent:a#1/render", "batch:b.issues", "now#1", "undo:write:w#1", holdStep}))
	assert.False(t, found, "this build records every step")

	tests := map[string]struct {
		names        []string
		step, module string
	}{
		"a kind of no module":        {[]string{"claim", "wait:w#1", "unknown_kind:u#1", "other_kind:o#1"}, "unknown_kind:u#1", ""},
		"the scope check of a merge": {[]string{"claim", "scope-check:m#1", "merge:m#1"}, "scope-check:m#1", moduleRound},
		"the merge of one pull":      {[]string{"merge:m#1"}, "merge:m#1", moduleLand},
		"the builtin before_review":  {[]string{"before_review:b#1"}, "before_review:b#1", moduleGate},
		"the builtin item_assign":    {[]string{"item_assign:a#1"}, "item_assign:a#1", moduleTracker},
		"the builtin pr_assign":      {[]string{"pr_assign:a#1"}, "pr_assign:a#1", moduleForge},
		"the builtin board_sync":     {[]string{"board_sync:a#1"}, "board_sync:a#1", moduleForge},
		"the owner merge wait":       {[]string{"DBOS.sleep", "watch"}, "watch", moduleCore},
	}

	for name, test := range tests {
		step, module, found := unreplayableStep(test.names)
		assert.True(t, found, name)
		assert.Equal(t, test.step, step, name)
		assert.Equal(t, test.module, module, name)
	}

	_, _, found = unreplayableStep(nil)
	assert.False(t, found)
}

func TestChangedModule_OnlyAModuleThatTheRunUsedCounts(t *testing.T) {
	t.Parallel()

	was, _ := parseVersions(versionWithModule("", moduleGate, gateVersion-1)[1:])
	now, _ := parseVersions(versionWithModule("", moduleGate, gateVersion)[1:])

	_, changed := changedModule(modulesUsed([]string{"item_read:own#1", "item_comment:c#2"}), was, now)
	assert.False(t, changed, "a run that recorded only tracker steps does not use the gate module")

	module, changed := changedModule(modulesUsed([]string{"item_read:own#1", "gate:g#1"}), was, now)
	assert.True(t, changed)
	assert.Equal(t, moduleGate, module)

	module, changed = changedModule([]string{moduleHost}, was, map[string]int{})
	assert.True(t, changed, "a module that this build does not hold has no version, so it differs")
	assert.Equal(t, moduleHost, module)
}

func TestDescribeModule_NamesTheVersionOrItsAbsence(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "version 2", describeModule("gate", map[string]int{"gate": 2}))
	assert.Equal(t, "no version", describeModule("gate", map[string]int{}))
}
