package flow

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// The builtins of this package belong to eight modules that move out of it one by one. Each module has a version of its recorded steps.
// The application version holds all eight, so a change to the steps of one module strands only the runs that recorded a step of that module.
// A change to a builtin that changes its recorded steps bumps the version of its module in moduleTable, and no other.
const (
	moduleCore    = "core"
	moduleAgent   = "agent"
	moduleTracker = "tracker"
	moduleForge   = "forge"
	moduleLand    = "land"
	moduleGate    = "gate"
	moduleHost    = "host"
	moduleRound   = "round"
)

// Step kinds that no other file names as a constant.
const (
	kindPreflight = "preflight"
	// kindBatch starts the names of the steps of merge, which has no counted step of its own.
	kindBatch = "batch"
	// kindUndo starts the name of the step that runs the undo of a write: undo:<the name of the write step>.
	kindUndo = "undo"
	// kindHold starts the name of the step that reads the hold of an item at the start of a run that spawn started (holdStep).
	kindHold = "hold"
	// retiredWatchKind is the read of a ready pull request in the wait of the single merge for the owner to merge. Only an older build records it.
	retiredWatchKind = "watch"
)

// coreVersion is the version of the core module. Version 2 added the vars field to the result of facts. Version 3 removed the round_* fields from it.
// Version 4 records the wait:notify step at every wait, before the receive of the answer. Version 5 removed the method and board fields from the result of facts.
// Version 6 adds the runners field to the result of facts. Version 7 adds the run and run_item fields to the input of the host job of a command call with a slot class.
// Version 8 adds the sleep step, with its marker record sleep:<name>#<n>/start.
// Version 9 adds the publish and progress steps, and the cost_usd field to each outcome that the gather step records.
// Version 10 adds the recv and send steps of the inbox.
// Version 11 adds the attempts and error fields to the result of command, the attempts field to write, and the retries of a spawned run to gather.
// Version 12 adds the cancel step, the deadline step of a gather, the deadline that each wait of a gather records and the open field of a gather result.
// Version 13 replaces the reports_skills field of each entry of the runners field of facts with the loads_skills field.
// Version 14 adds the first_of step with its deadline step and its look steps, which record every run that ended in the read, and a child run sends an end notice to its parent.
// Version 15 moves the answers of wait onto the inbox as messages of the topic beehave.answer, and a wait with a timeout and no people records its deadline step.
// Version 16 records the topic of a recv call with its deadline, in the deadline step.
// Version 17 records the run that a send to an item names, in the step send:<name>#<n>.to.
// Version 18 adds the stream step with its deadline step, which records {until, run}: the end of the wait and the child run that the call reads.
// Version 19 runs the host job of a command call with a host and no slot class in the default class, so the call queues a child run before its command step.
// Version 20 adds the earlier_reads step.
const coreVersion = 20

// agentVersion is the version of the agent module. Version 2 added the limit field to the phase output, and the limit step and the later looks at the caps
// that an agent call records when a provider limit stops its run. Version 3 added the start_head field to the phase input, the session and diff_stat fields
// to the record of a lost step, and the resumed field to the phase output. Version 4 added the tree_start field to the phase input.
// Version 5 writes the cost of a failed run to the stream of failed runs in place of its spent step.
// Version 6 adds the agent/caps step to the phase, the call field of the phase input and the cap_stop field of the phase output.
// Version 8 added the note field to the limit record, the limited field to the phase input and the resume_of field to the phase output, so the rerun after a provider limit resumes the limited session.
// Version 9 adds the skills field to the result of an agent call, and records the label of the call in place of its kind.
// Version 10 removed the allow_write list from the recorded sandbox of an agent call.
// Version 11 adds the data keyword: the prompt_data output of the render step and the data and data_file fields of the phase input.
// Version 12 adds the committed field to the result of an agent call.
// Version 13 puts the data file in a folder of its own and removes the data_file field of the phase input: the phase derives the path.
// Version 14 adds the skills output of the render step and the skills field of the phase input, and removes the skills list from the recorded sandbox: the prompt declares the skills.
// Version 15 adds the start mark step of an editing call, and the commit step after its read step, in a project with a repository.
// Version 16 removes the event that carried the cost of a failed run to the queuing run: the cost is read from the record of the failed phase.
const agentVersion = 16

// landVersion is the version of the land module. Version 2 added the approved field to each member in the recorded input of the batch steps.
// Version 3 added the refusal field to the result of a merge, which the reason of a batch member reads.
// Version 4 records the host job of the project gate of a batch in its own step (batch_gate:job) before the gate waits, records a lost job
// that has a proof in the item run (batch:<call>.gate#<n>/host-loss, and @2 for the rerun), and adds host_loss and host_losses to the result of merge.
// Version 5 merges with one call: `merge` is the old `batch_merge`, and a merge of one pull request records the batch steps. The step kind merge is gone.
// Version 6 records the scope check of each member (batch:<call>.scope#<item>) before its intent.
// Version 7 adds the moved field to the result of merge, and the hand-off steps batch:<call>.ready#<item> and batch:<call>.watch#<item>@<round>.
// Version 8 removed the foreign, pr, head and author fields from the record of the preflight step.
// Version 9 adds the run and run_item fields to the input of the merge of a member.
// Version 10 adds the step batch:<call>.kept#<item> after an owner line that keeps issues, and the kept field to the record of the scope check.
// Version 11 records each issue of the scope as text: "#N" for the repository of the pull request and "owner/repo#N" for another repository. A record of version 10 holds numbers and still reads.
// Version 12 adds the scope step at the first review read (preflight) and at the first merge call of a run that no gate fixed a scope for, and the no_forge field to the record of the scope step.
// Version 13 records the closes field of each member in the input of the batch steps, and the merge audits its closing references against it.
// Version 14 adds the call of merge with `member`: it records the step batch:<call>.tip after the merge, and the moved field of the intent step, when the tip has another tree than expect_tree.
// Version 15 adds the kept and closing fields of each member to the input of the batch steps. The merge no longer records the steps batch:<call>.scope#<item> and batch:<call>.kept#<item>: the scope check is the library scope.star.
const landVersion = 15

// trackerVersion is the version of the tracker module. Version 2 retires the kind item_assign, which version 1 recorded. item_create takes an assignee.
// Version 3 adds the item_stop step. Version 4 adds the item_list step.
const trackerVersion = 4

// forgeVersion is the version of the forge module. Version 2 added the pr_state step. Version 3 retires the kind pr_assign, which version 2 recorded.
// Version 4 retires the kind board_sync, which version 3 recorded. Version 5 adds the push step. Version 6 adds the pr_history step.
// Version 7 adds the steps board_card, integration_pull, pr_take, round_cut, round_land and round_sync, which the engine runs as the writer. The remedy tool no longer runs them as command steps.
// Version 8 adds the pr_read and issue_assignees steps. Version 9 adds the pr_list step. Version 10 adds the pr_closing step. Version 11 adds the base_tip and issue_state steps.
const forgeVersion = 11

// gateVersion is the version of the gate module. Version 3 records a lost host job in the gate step, then the wait for its proof (gate:lost-start, gate:lost-look.<k>
// with a wait field), and in the item run the read of the proof after each answer of the owner (<call>/lost-ask.<n>).
// At the wait limit the host job run ends with no failure and records the lost job with its wait.
// Version 4 records a lost job that has a proof in the item run (<call>/host-loss), and the gate step records host_losses, the count of the host losses of the item tree.
// Version 5 added the revert field to the results of gate, and the revert run to the checks of a candidate gate.
// Version 6 retires the kind before_review, which version 5 recorded. The function before_review of the library judges a head with command steps.
// Version 7 adds the step field to the result of gate: the name of the step that recorded it.
const gateVersion = 7

// roundVersion is the version of the round module. Version 2 added the step of the engine that makes the worktree at the start of every round item.
// No builtin records that step, so a move to this build compares the round module for every run of a round item (requireSameRoundModule).
// Version 3 adds the pulls field to the result of queue and the labels field to each of its rows.
// Version 4 adds the state stopped and the fields stop_step and stop_reason to each row.
// Version 5 adds the field value to each row: the value that the last run of a done row returned.
// Version 6 retires the kind queue, which version 5 recorded. The function queue of the library reads with the steps item_list, pr_list, run_read and pr_state.
// Version 7 retires the kind scope, which version 6 recorded. The library scope.star fixes and checks the scope with the steps pr_closing, earlier_reads and wait.
const roundVersion = 7

// stepModule is one module of the table: its version, the step kinds that it records, and the whole step names that it records with no kind in front.
// The kinds are the kinds of its builtins. The engine kinds are kinds that the engine records for a builtin of the module, under a name that no builtin has.
// The retired kinds are kinds that an older build recorded for the module and this build does not record. They keep their module, so a refusal can name it.
// The steps are the steps of its waits that no kind names: the receive of an answer.
type stepModule struct {
	name    string
	version int
	kinds   []string
	engine  []string
	retired []string
	steps   []string
}

// moduleTable assigns each recorded step kind to its module. The order is the order of the application version.
func moduleTable() []stepModule {
	return []stepModule{
		{
			name:    moduleCore,
			version: coreVersion,
			kinds:   []string{cancelKind, "command", "earlier_calls", earlierReadsKind, envKeyword, "facts", firstOfKind, gatherKind, "now", progressKind, publishKind, recvKind, sendKind, sleepKind, "spawn", streamKind, "wait", "write"},
			engine:  []string{kindUndo},
			retired: []string{retiredWatchKind},
			steps:   []string{receiveStepName},
		},
		{name: moduleAgent, version: agentVersion, kinds: []string{agentStepName, loginStepName}},
		{name: moduleTracker, version: trackerVersion, kinds: []string{"item_comment", "item_create", itemListKind, "item_read", "item_status", itemStopKind}, retired: []string{"item_assign"}},
		{name: moduleForge, version: forgeVersion, kinds: []string{baseTipKind, boardCardKind, issueAssigneesKind, issueStateKind, prClosingKind, "pr_comment", prHistoryKind, prListKind, "pr_open", prReadKind, prStateKind, prTakeKind, pushKind, roundCutKind, roundIntegrationKind, roundLandKind, roundSyncKind}, retired: []string{"board_sync", "pr_assign"}},
		{name: moduleLand, version: landVersion, kinds: []string{kindBatch, kindPreflight}, retired: []string{"merge"}},
		{name: moduleGate, version: gateVersion, kinds: []string{kindGate}, retired: []string{"before_review"}},
		{name: moduleHost, version: 1, kinds: []string{"host_teardown", "host_up"}},
		{name: moduleRound, version: roundVersion, kinds: []string{runReadKind}, engine: []string{kindHold}, retired: []string{"queue", "scope", "scope-check"}},
	}
}

// moduleOfKind returns the module that records a step kind, and false for a kind that no module records.
func moduleOfKind(kind string) (string, bool) {
	for _, module := range moduleTable() {
		if slices.Contains(module.kinds, kind) || slices.Contains(module.engine, kind) || slices.Contains(module.retired, kind) {
			return module.name, true
		}
	}

	return "", false
}

// moduleVersions is the list of module versions of this build, such as core7.agent12.tracker2.forge6.land9.gate6.host1.round3.
func moduleVersions() string {
	table := moduleTable()
	parts := make([]string, len(table))

	for i, module := range table {
		parts[i] = module.name + strconv.Itoa(module.version)
	}

	return strings.Join(parts, ".")
}

// moduleVersionPart matches one module and its version in a stored list of module versions.
var moduleVersionPart = regexp.MustCompile(`^([a-z]+)([0-9]+)$`)

// parseVersions reads the steps part of a stored application version as one version for each module.
// The single version of the builtin steps that came before the modules (stepsVersion) stands for every module at version 1.
// Any other single version (such as star13), a part that is not the version of a module of the table, and an empty text are not readable.
// A build tag such as rc1 has the shape of a module version, so only the names of the table decide.
func parseVersions(steps string) (map[string]int, bool) {
	versions, err := readVersions(steps)

	return versions, err == nil
}

// readVersions is parseVersions with the reason: the error names the bad entry of a list, or the module that a list names twice.
func readVersions(steps string) (map[string]int, error) {
	if steps == stepsVersion {
		return allModulesAtOne(), nil
	}

	versions := map[string]int{}

	for part := range strings.SplitSeq(steps, ".") {
		name, number, ok := moduleVersionOf(part)
		if !ok {
			return nil, fmt.Errorf("the entry %q of the step list %q is not the name of a module and its version, such as gate2", part, steps)
		}

		if _, twice := versions[name]; twice {
			return nil, fmt.Errorf("the step list %q names the module %s twice", steps, name)
		}

		versions[name] = number
	}

	return versions, nil
}

// allModulesAtOne is the list that the single version of the steps stands for.
func allModulesAtOne() map[string]int {
	versions := map[string]int{}

	for _, module := range moduleTable() {
		versions[module.name] = 1
	}

	return versions
}

// moduleListShape matches a text of entries that each have a name and an optional number, joined by dots.
var moduleListShape = regexp.MustCompile(`^[a-z]+[0-9]*(\.[a-z]+[0-9]*)*$`)

// looksLikeModuleList is true for a text that starts with the name of a module and has the shape of a list of module versions, even when an entry is
// malformed or a module repeats. A build tag such as rc1 does not start with the name of a module, so it is not a list.
func looksLikeModuleList(text string) bool {
	if !moduleListShape.MatchString(text) {
		return false
	}

	first, _, _ := strings.Cut(text, ".")
	first = strings.TrimRight(first, "0123456789")

	return slices.ContainsFunc(moduleTable(), func(module stepModule) bool { return module.name == first })
}

// moduleVersionOf reads one part of a stored list, such as gate2. The name must be a module of the table.
func moduleVersionOf(part string) (name string, number int, ok bool) {
	match := moduleVersionPart.FindStringSubmatch(part)
	if match == nil || !slices.ContainsFunc(moduleTable(), func(module stepModule) bool { return module.name == match[1] }) {
		return "", 0, false
	}

	number, err := strconv.Atoi(match[2])

	return match[1], number, err == nil
}

// kindOfStep returns the step kind that a recorded step name starts with. The names are `<kind>:<label>#<n>`, with a sub-step after a slash
// and a round after an at sign. A name that starts with no kind of a module, such as claim or DBOS.sleep, is not a step of a module.
func kindOfStep(name string) (string, bool) {
	kind, _, _ := strings.Cut(name, "/")

	kind = kind[:strings.IndexAny(kind+":", ":#@")]

	_, known := moduleOfKind(kind)

	return kind, known
}

// receiveStepName is the name that DBOS gives to the recorded receive of an answer of the owner. Every wait for an answer records it.
const receiveStepName = "DBOS.recv"

// moduleOfStep returns the module that recorded a step: the module that names the whole step, else the module of the kind that the name starts with.
func moduleOfStep(name string) (string, bool) {
	for _, module := range moduleTable() {
		if slices.Contains(module.steps, name) {
			return module.name, true
		}
	}

	kind, known := kindOfStep(name)
	if !known {
		return "", false
	}

	return moduleOfKind(kind)
}

// dbosStepPrefix starts the names of the steps that DBOS records for a workflow, such as DBOS.sleep and DBOS.getResult.
const dbosStepPrefix = "DBOS."

// engineStep reports whether the engine records a step under the name for every run, apart from any module: the read of the item, the claim and its end,
// the worktree, the cancel of other runs, the start of a child run, and a step that DBOS records.
func engineStep(name string) bool {
	engine := []string{
		itemName, string(model.StepClaim), string(model.StepReady), string(model.StepRelease), treeStepName, treeMakeStepName, othersStep,
		phaseName, exclusiveName, hostJobName,
	}

	return strings.HasPrefix(name, dbosStepPrefix) || slices.Contains(engine, name)
}

// recordedHere reports whether this build records a step under the name: a step of the engine, a whole step of a module, or a step whose kind
// a builtin of a module or the engine records for it. A retired kind is not recorded here.
func recordedHere(name string) bool {
	kind, _ := kindOfStep(name)

	return engineStep(name) || slices.ContainsFunc(moduleTable(), func(module stepModule) bool {
		return slices.Contains(module.steps, name) || slices.Contains(module.kinds, kind) || slices.Contains(module.engine, kind)
	})
}

// unreplayableStep returns the first step that this build does not record, with the module of the older build that recorded it.
// The module is empty for a step that no module claims. A run that recorded such a step cannot replay here, whatever the module versions are.
func unreplayableStep(names []string) (step, module string, found bool) {
	for _, name := range names {
		if !recordedHere(name) {
			module, _ = moduleOfStep(name)

			return name, module, true
		}
	}

	return "", "", false
}

// modulesUsed returns the modules that recorded one of the steps, in the order of the table. The waits of the owner count as steps of the core module.
func modulesUsed(names []string) []string {
	used := map[string]bool{}

	for _, name := range names {
		if module, known := moduleOfStep(name); known {
			used[module] = true
		}
	}

	var ordered []string

	for _, module := range moduleTable() {
		if used[module.name] {
			ordered = append(ordered, module.name)
		}
	}

	return ordered
}

// changedModule returns the first of the modules whose version in was is not the version in now. A module that a list does not hold has no version, so it differs.
func changedModule(modules []string, was, now map[string]int) (string, bool) {
	for _, module := range modules {
		old, hadOld := was[module]
		current, hasCurrent := now[module]

		if !hadOld || !hasCurrent || old != current {
			return module, true
		}
	}

	return "", false
}

// describeModule names the version of a module in a list, for a message.
func describeModule(module string, versions map[string]int) string {
	if number, ok := versions[module]; ok {
		return fmt.Sprintf("version %d", number)
	}

	return "no version"
}
