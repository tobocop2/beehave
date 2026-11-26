package remediation_test

import (
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"text/template/parse"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rule is one rule of the brief text that the engine once built from embedded files. The phrase is a short distinctive piece of the rule, and each prompt in `prompts` must hold it.
type rule struct {
	from    string
	phrase  string
	prompts []string
}

const (
	implement = "implement"
	fix       = "fix"
	plan      = "plan"
	review    = "review"
)

// everyLane lists the prompts of the agents that change or judge the product.
func everyLane() []string { return []string{implement, fix, plan, review} }

// workLanes lists the prompts of the agents that write code or run builds.
func workLanes() []string { return []string{implement, fix} }

// workAndReview lists the prompts of the agents that run shell commands and builds.
func workAndReview() []string { return []string{implement, fix, review} }

func acceptanceRules() []rule {
	return []rule{
		{"acceptance_rules", "Run the acceptance script before your first edit", workLanes()},
		{"acceptance_rules", "Run it again before you stop", workLanes()},
		{"acceptance_rules", "the change has no output: say in the report how you know", workLanes()},
		{"acceptance_rules", "its `## Expected behavior` table is the acceptance", workLanes()},
		{"acceptance_rules", "take the one that changes fewer cells", workLanes()},
		{"acceptance_rules", "a wider rule is the owner's decision", workLanes()},
		{"before_write_rules", "Check for an existing pull request, at the start and again before", []string{implement, fix, plan}},
		{"before_write_rules", "Repeat-offender check", []string{implement, fix, plan}},
		{"before_write_rules", "Re-derive every cited site at the tip you branch from", []string{implement, fix, plan}},
		{"before_write_rules", "Build-versus-buy search. Before you write a new mechanism", []string{implement, fix, plan}},
		{"call_site_rules", "Remove the feature WHERE THE PRODUCT INVOKES IT", workLanes()},
		{"item_skeleton", "The engine has claimed this item", workLanes()},
		{"item_skeleton", "{{ .item.body }}", everyLane()},
		{"item_skeleton", "Name the tempting wrong fix and why it is wrong", []string{implement, fix, plan}},
		{"item_skeleton", "Read the files that the item names before you start, and no others", workLanes()},
	}
}

func reportRules() []rule {
	return []rule{
		{"closing_rules", "Write the full report to the report file that the run names for this lane FIRST", []string{plan, review}},
		{"lane_report_check", "Write the full report as the output `report`", workLanes()},
		{"lane_report_check", `a line says "no background commands left"`, workLanes()},
		{"lane_uncaused_red", "Write the output `uncaused` as a text that holds a JSON list", workLanes()},
		{"lane_uncaused_red", "a red that the base has too and that no hunk of yours touches", workLanes()},
		{"closing_rules", "The report carries four tables", everyLane()},
		{"closing_rules", "`## Claims`: every claim in the title, body, commit messages", everyLane()},
		{"closing_rules", "`## Hunks`: every changed production hunk, the arm that removes it", everyLane()},
		{"closing_rules", "`## Inputs`: every input class the changed code accepts or refuses", everyLane()},
		{"closing_rules", "`## Siblings`: every other site with the same behavior", everyLane()},
		{"closing_rules", "under 300 words, yourself", everyLane()},
		{"closing_rules", "what is owed, in order; the one thing not to redo", everyLane()},
		{"closing_rules", "VERDICT LINE plus at most six lines", everyLane()},
		{"closing_rules", "Send no separate message with the report", everyLane()},
		{"closing_rules", "write the handoff to the handoff file that the run names", everyLane()},
		{"closing_rules", "Regressions of this pull request so far", everyLane()},
		{"closing_rules", "Tokens at handoff", everyLane()},
		{"lane_names", "the item-specific numbers that the run names for this lane", everyLane()},
		{"handoff_first_rules", "Read a log only by its summary lines", everyLane()},
		{"project_rules_heading", "Follow the rules file of the project", everyLane()},
	}
}

func evidenceRules() []rule {
	return []rule{
		{"evidence_rules", "Every absence check runs with its positive control IN THE SAME CALL", everyLane()},
		{"evidence_rules", "Never `2>/dev/null` a command whose empty output", everyLane()},
		{"evidence_rules", "A control must exercise the layer the product uses", everyLane()},
		{"evidence_rules", "A verification derived from the same pattern as the edit", everyLane()},
		{"evidence_rules", "AN ISSUE'S ROOT CAUSE AGES WITH ITS PINNED DEPENDENCIES", everyLane()},
		{"evidence_rules", "LIST THE FILES IT TRAVERSES", everyLane()},
		{"evidence_rules", "find where its variable is SET", everyLane()},
		{"precedence_rules", "A precedence test needs a LOSING field", []string{implement, fix, review}},
		{"precedence_rules", "Enumerate a duplicated CLAIM by its substance", []string{implement, fix, review}},
		{"precedence_rules", "Key every snapshot on the FULL PATH", []string{implement, fix, review}},
		{"gate_step_rules", "Run the tests that the change touches by name, in the work folder", workLanes()},
		{"gate_step_rules", "The engine runs the gate and the mutation run for each candidate", workAndReview()},
		{"gate_step_rules", "Run no gate step yourself, and write no job script for one", workAndReview()},
		{"gate_step_rules", "A result counts only with the number of tests that ran", workAndReview()},
	}
}

func designRules() []rule {
	return []rule{
		{"design_rules", "This is a DESIGN lane: the rule and the acceptance script, no product code", []string{plan}},
		{"design_rules", "every code path the item has", []string{plan}},
		{"design_rules", "every option mode", []string{plan}},
		{"design_rules", "every context that must NOT change", []string{plan}},
		{"design_rules", "the CI's own checks with the CI's base", []string{plan}},
		{"design_rules", "Use a generator for the inputs, and an oracle that is not the code under test", []string{plan}},
		{"design_rules", "Report the script path", []string{plan}},
		{"design_rules", "write the expected-behavior table first", []string{plan}},
		{"design_rules", "Report each cell where the issue leaves the answer open", []string{plan}},
		{"design_rules", "Write the paths of the harness and of the build of the base into the same file", []string{plan}},
		{"papers_rules", "run the proof, and write the report and the pull request body", []string{plan}},
		{"papers_rules", "Change no source path", []string{plan}},
		{"papers_rules", "report it with the red row", []string{plan}},
	}
}

func shellRules() []rule {
	return []rule{
		{"handoff_first_rules", "Never pass a script string to a shell", workAndReview()},
		{"handoff_first_rules", "A loop that needs POSIX word splitting", workAndReview()},
		{"shell_rules", "Put no delete in any command you run", workAndReview()},
		{"shell_rules", "never `sed -i.bak`", workAndReview()},
		{"long_jobs_rules", "Never end your turn waiting on a job you started yourself", workAndReview()},
		{"long_jobs_rules", "Before you end, stop each background command you started", workAndReview()},
		{"long_jobs_rules", "Wait loops have a timeout", workAndReview()},
		{"long_jobs_rules", "Report no background commands left", workAndReview()},
	}
}

func compiledAndUnionRules() []rule {
	return []rule{
		{"compiled_rules", "ASSERTED INVALID UTF-8", workAndReview()},
		{"compiled_rules", "A restore is not green until something forces the rebuild", workAndReview()},
		{"compiled_rules", "assert a NONZERO count", workAndReview()},
		{"compiled_rules", "Name the FEATURES beside the target", workAndReview()},
		{"compiled_rules", "Read the package name out of its own manifest", workAndReview()},
		{"union_rules", "count what came from NEITHER side", workAndReview()},
		{"union_rules", "THE LINE CHECKS ARE NOT THE GATE", workAndReview()},
		{"union_rules", "Be most suspicious when a branch ADDS an implementor", workAndReview()},
		{"union_rules", "enumerate that trait's implementors AT THE TIP", workAndReview()},
		{"union_rules", "compares FILE CONTENT, never two added-line sets", workAndReview()},
		{"union_rules", "assert the blob", workAndReview()},
	}
}

func reviewRules() []rule {
	only := []string{review}

	return []rule{
		{"review_rules", "not iterated to zero", only},
		{"review_rules", "Every added test gets a mutation arm", only},
		{"review_rules", "Cite the reference the ruling names", only},
		{"review_rules", "Check the state the change stores", only},
		{"review_rules", "Check for dead code", only},
		{"review_rules", "Build-versus-buy search. When the change builds a mechanism", only},
		{"review_rules", "A SOURCE-CONTRACT TEST IS NOT PROVEN UNTIL A WRONG IMPLEMENTATION FAILS IT", only},
		{"review_rules", "suspect the arm before the code", only},
		{"review_rules", "Apply at least one arm AT THE CALL SITE", only},
		{"review_rules", "The call site comes from the ISSUE'S SENTENCE", only},
		{"review_rules", "Reuse the implementer's proven build", only},
		{"review_rules", "Read the implementer's report as a CLAIM", only},
		{"review_rules", "compare EVERY changed cell with the BASE", only},
		{"review_rules", "The matrix harness and the build of the base belong to the item", only},
		{"review_rules", "Read every review body and every inline comment", only},
		{"review_rules", "This step is given no gate result, so state no gate result as fact", only},
		{"review_rules", "Condition 4, every item, unasked", only},
		{"review_rules", "MERGE-BASE content against head content", only},
		{"review_rules", "Plant an AWS key pair as a positive control", only},
	}
}

func gateEvidenceRules() []rule {
	return []rule{
		{"gate_evidence", "Capture the exit code before any pipe", workAndReview()},
		{"gate_evidence", "gives its stdin to every command inside it", workAndReview()},
		{"gate_evidence", "A regeneration is evidence for its own tree only", workAndReview()},
		{"gate_evidence", "assert the loop count against the input count", workAndReview()},
		{"gate_evidence", "search the diff for `TODO` and `FIXME`", workLanes()},
		{"gate_evidence", "names its author, as `TODO(name)`", workLanes()},
	}
}

func reviewFormatRules() []rule {
	return []rule{
		{"review_format", "Read the existing review first", []string{review}},
		{"review_format", "Label each finding NEW or ALREADY RAISED", []string{review}},
		{"review_format", "The report ends with three buckets", []string{review}},
		{"review_format", "Write `none` for an empty bucket", []string{review}},
		{"review_format", "The run names one of three modes", []string{review}},
		{"review_format", "In take-over mode, you review a pull request of another author, and you fix nothing", []string{review}},
		{"review_format", "In audit mode, you re-review work that is already merged, and each finding becomes a fix item", []string{review}},
		{"review_format", "In queue mode, you review the change of a lane that did not write it, and the verdict gates the merge", []string{review}},
	}
}

func briefRules() []rule {
	return []rule{
		{"brief_rules", "Read an issue or pull request thread from the thread file", everyLane()},
		{"brief_rules", "A reproduction is self-contained", everyLane()},
	}
}

func mergeRules() []rule {
	return []rule{
		{"merge_rules", "Make the first commit when the change is green", []string{implement}},
		{"merge_rules", "the engine does these after the push", []string{implement}},
		{"merge_rules", "the engine owns both", []string{fix}},
		{"merge_rules", "Never use the merge queue of GitHub", []string{implement, fix, review}},
		{"merge_rules", "Known base reds are in one file", workAndReview()},
		{"merge_rules", "A finding that survives the review is filed before the merge", []string{review}},
		{"merge_rules", "a change that is not committed is lost with the lane", workLanes()},
	}
}

func laneLayoutRules() []rule {
	return []rule{
		{"lane_layout", "A fix lane is not a proof lane and not a review lane", workLanes()},
		{"lane_layout", "Run the scoped tests, not the full gate. Before your last commit, run the red test of each finding and the tests of the packages that your diff reaches", []string{fix}},
		{"lane_layout", "One reviewer judges one pull request", []string{review}},
		{"lane_layout", "A rise in the regression count blocks the lane", workAndReview()},
		{"lane_layout", "Patch rounds are bounded for each pull request", workLanes()},
		{"lane_layout", "The settle lane is the only edit after the last round", workLanes()},
		{"lane_layout", "Write the output `regressions` as a whole number", []string{plan, review}},
		{"lane_layout", "Count only the regressions that are open at the head you review: a defect that a later lane fixed is not open", []string{review}},
		{"lane_layout", "Count only the regressions that are open at the head you plan from", []string{plan}},
		{"lane_layout", "the number of regressions open at your head", everyLane()},
	}
}

func reviewFramingRules() []rule {
	return []rule{
		{"attack_list", "Write the attack list for this diff before you read it closely", []string{review}},
		{"validator_review", "Frame a security review of our own validators as verification", []string{review}},
		{"hand_over", "When the author hands the branch over with terms, the terms win", []string{"take_review"}},
	}
}

const (
	queue      = "queue"
	takeReview = "take_review"
)

func laneBoundaryRules() []rule {
	return []rule{
		{"lane_boundaries", "Each lane has one item, one worktree, one branch and one pull request", workLanes()},
		{"lane_boundaries", "Never merge, never comment on the forge, and never file an issue", workLanes()},
		{"lane_boundaries", "Never touch the worktree of another lane", workLanes()},
	}
}

func skillOrderRules() []rule {
	return []rule{
		{"skill_rules", "Invoke each skill that the rules name with the Skill tool, by name", everyLane()},
		{"skill_rules", "a report with no Skill call goes back unread", everyLane()},
		{"skill_rules", "{{ skill \"plan-vs-conventions\" }}` for a change past one file, `{{ skill \"cross-cutting-api-change\" }}` for a new variant, field or entry point", workLanes()},
		{"skill_rules", "then `{{ skill .facts.vars.review_skill }}` iterated to zero before the gate", workLanes()},
	}
}

func briefChoiceRules() []rule {
	return []rule{
		{"brief_choice_rules", "A ruling of the owner binds the item", everyLane()},
		{"brief_choice_rules", "Never reverse a ruling", everyLane()},
		{"brief_choice_rules", "is the floor of every lane, never the whole answer", everyLane()},
		{"brief_choice_rules", "Choose the other skills from what the item is", everyLane()},
		{"acceptance_rules", "take the one that changes fewer cells", workLanes()},
	}
}

func baseRedRules() []rule {
	return []rule{
		{"base_red_rules", "A base-red entry names the command that CI runs, not the test target", workAndReview()},
		{"base_red_rules", "The exclusion list of a guard is a decision that expires", workAndReview()},
		{"base_red_rules", "justify the exclusion again before you add a case", workAndReview()},
	}
}

func disjointnessRules() []rule {
	return []rule{
		{"disjointness_rules", "A claim that two changes are disjoint carries the SHA it was measured at", workAndReview()},
		{"disjointness_rules", "measure again at the tip you merge to", workAndReview()},
	}
}

func queueScopeRules() []rule {
	return []rule{
		{"queue_scope_rules", "An item is the scope of one pull request, not one issue", []string{queue}},
		{"queue_scope_rules", "share a mechanism, a code area or a fix", []string{queue}},
		{"queue_scope_rules", "Batch by mechanism, not by the number of the issue", []string{queue}},
		{"queue_scope_rules", "The scope of the item is fixed at the first review", workLanes()},
	}
}

func overtakenRules() []rule {
	both := []string{queue, takeReview}

	return []rule{
		{"triage_rules", "has overtaken a pull request before you ask how to resolve it", both},
		{"triage_rules", "never from the titles", both},
		{"triage_rules", "what the close loses", both},
		{"triage_rules", "what a wrong resolution costs", both},
	}
}

func stackedBranchRules() []rule {
	return []rule{
		{"stacked_branch_rules", "Do not edit a branch that a pending merge depends on", workLanes()},
		{"stacked_branch_rules", "File the finding that survives as an issue first", workLanes()},
	}
}

// loadPrompts reads the named prompts and squashes each one.
func loadPrompts(t *testing.T, names ...string) map[string]string {
	t.Helper()

	texts := map[string]string{}
	folder := os.DirFS("prompts")

	for _, name := range names {
		raw, err := fs.ReadFile(folder, name+".md")
		require.NoError(t, err)

		texts[name] = squash(string(raw))
	}

	return texts
}

// checkRules fails for each phrase that a prompt of the rule lost, and returns the number of prompt checks it made.
func checkRules(t *testing.T, texts map[string]string, group []rule) int {
	t.Helper()

	held := 0

	for _, lost := range group {
		for _, name := range lost.prompts {
			held++

			// A plain check: a failed assert.Contains would print the whole prompt.
			if !strings.Contains(texts[name], lost.phrase) {
				t.Errorf("prompts/%s.md lost the phrase %q of the rule in %s", name, lost.phrase, lost.from)
			}
		}
	}

	return held
}

// holdGroup checks one group of rules against every prompt that the group names.
func holdGroup(t *testing.T, group []rule) {
	t.Helper()

	texts := loadPrompts(t, append(everyLane(), takeReview, queue)...)

	assert.Positive(t, checkRules(t, texts, group))
}

func TestPrompts_HoldTheLaneBoundaries(t *testing.T) {
	t.Parallel()
	holdGroup(t, laneBoundaryRules())
}

func TestPrompts_HoldTheSkillOrder(t *testing.T) {
	t.Parallel()
	holdGroup(t, skillOrderRules())
}

func TestPrompts_HoldTheBriefChoiceRules(t *testing.T) {
	t.Parallel()
	holdGroup(t, briefChoiceRules())
}

func TestPrompts_HoldTheBaseRedRules(t *testing.T) {
	t.Parallel()
	holdGroup(t, baseRedRules())
}

func TestPrompts_HoldTheDisjointnessShaRule(t *testing.T) {
	t.Parallel()
	holdGroup(t, disjointnessRules())
}

func TestPrompts_HoldTheQueueScopeRule(t *testing.T) {
	t.Parallel()
	holdGroup(t, queueScopeRules())
}

func TestPrompts_HoldTheOvertakenJudgment(t *testing.T) {
	t.Parallel()
	holdGroup(t, overtakenRules())
}

func TestPrompts_HoldTheNeedsUserRule(t *testing.T) {
	t.Parallel()

	texts := loadPrompts(t, workLanes()...)
	group := []rule{
		{"needs_user_rules", "Write the output `hold` when the item needs the owner", workLanes()},
		{"needs_user_rules", "the keys `question`, `options` and `recommendation`", workLanes()},
		{"needs_user_rules", "A note with no recommendation, or with fewer than two options, fails the run", workLanes()},
		{"needs_user_rules", `"uncaused":"[]","hold":""`, workLanes()},
		{"needs_user_rules", "write the output `hold` and stop: a wider rule is the owner's decision", workLanes()},
	}

	assert.Positive(t, checkRules(t, texts, group))

	for _, name := range workLanes() {
		for _, old := range []string{"stop and report it: a wider rule", "or the script is wrong: stop and report it"} {
			// A plain check: a failed assert.NotContains would print the whole prompt.
			if strings.Contains(texts[name], old) {
				t.Errorf("prompts/%s.md still says %q, a stop that no step reads", name, old)
			}
		}
	}
}

func TestPrompts_HoldTheStackedBranchRule(t *testing.T) {
	t.Parallel()
	holdGroup(t, stackedBranchRules())
}

// squash drops the bold marks and joins the words of a text with one space, so that a phrase matches across the line breaks and the bold spans of a prompt.
func squash(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "**", "")), " ")
}

func TestPrompts_HoldEveryRuleOfTheBriefText(t *testing.T) {
	t.Parallel()

	texts := loadPrompts(t, append(everyLane(), takeReview, queue)...)

	groups := [][]rule{acceptanceRules(), reportRules(), evidenceRules(), designRules(), shellRules(), compiledAndUnionRules(), reviewRules(), gateEvidenceRules(), reviewFormatRules(), briefRules(), mergeRules(), laneLayoutRules(), reviewFramingRules(), laneBoundaryRules(), skillOrderRules(), briefChoiceRules(), baseRedRules(), disjointnessRules(), queueScopeRules(), overtakenRules(), stackedBranchRules()}

	held, counted := 0, 0

	for _, group := range groups {
		counted += len(group)
		held += checkRules(t, texts, group)
	}

	// One phrase for each mapped or added row of the proof table that has a phrase of its own: a dropped phrase fails here.
	assert.Equal(t, 163, counted)
	assert.Positive(t, held)
}

// The engine opens the pull request, sets its body and links the stack (pr_open in workflow.star), so a work lane carries no rule that does the same.
func TestPrompts_TheWorkLanesLeaveThePullRequestToTheEngine(t *testing.T) {
	t.Parallel()

	for _, name := range workLanes() {
		raw, err := fs.ReadFile(os.DirFS("prompts"), name+".md")
		require.NoError(t, err)

		text := squash(string(raw))

		for _, gone := range []string{"Addresses #N", "Closes #N", "gh stack link", "native stack", "Open a draft pull request"} {
			assert.NotContains(t, text, gone, name)
		}
	}
}

// The agent of a sandboxed step has no credential, and the engine pushes after each step that commits (push in workflow.star and review_fix.star).
// So no prompt of such a step tells the agent to push. The control is the text of the base, which each pattern finds.
func TestPrompts_NoPromptOfAStepThatCommitsTellsTheAgentToPush(t *testing.T) {
	t.Parallel()

	tells := regexp.MustCompile(`git push|(^|[*] *|[.] )Push (the branch|every|at the first)|before you push|Before each push`)
	control := "**Push at the first checkpoint.** Push the branch when the first commit is green. Run `git push origin HEAD`. Before each push, search the diff."
	require.Len(t, tells.FindAllString(control, -1), 4, "the patterns find each push line of the base")

	for _, name := range []string{implement, fix, "sync"} {
		raw, err := fs.ReadFile(os.DirFS("prompts"), name+".md")
		require.NoError(t, err)

		for line := range strings.Lines(string(raw)) {
			assert.False(t, tells.MatchString(line), "prompts/%s.md tells the agent to push: %s", name, line)
		}
	}
}

// namesBuildHost reports whether a leaf of the template tree names build_host: a field, a variable field, a chained field or a string argument such as `index .facts "build_host"`.
func namesBuildHost(node parse.Node) bool {
	switch n := node.(type) {
	case *parse.FieldNode:
		return slices.Contains(n.Ident, "build_host")
	case *parse.VariableNode:
		return slices.Contains(n.Ident, "build_host")
	case *parse.ChainNode:
		return slices.Contains(n.Field, "build_host")
	case *parse.StringNode:
		return n.Text == "build_host"
	default:
		return false
	}
}

// readsBuildHost reports whether a template tree names build_host anywhere that a value is read: an action, a condition, a range, a with, a template call or an argument.
func readsBuildHost(node parse.Node) bool {
	return namesBuildHost(node) || slices.ContainsFunc(childNodes(node), readsBuildHost)
}

// childNodes lists the nodes that a template node holds: the body of a list, the pipe of an action or a template call, the commands of a pipe, the operand of a chain and the parts of a branch.
func childNodes(node parse.Node) []parse.Node {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return nil
		}

		return n.Nodes
	case *parse.ActionNode:
		return []parse.Node{n.Pipe}
	case *parse.TemplateNode:
		return []parse.Node{n.Pipe}
	case *parse.PipeNode:
		return commandNodes(n.Cmds)
	case *parse.CommandNode:
		return n.Args
	case *parse.ChainNode:
		return []parse.Node{n.Node}
	default:
		return branchNodes(node)
	}
}

// branchNodes lists the pipe and the two bodies of an if, a range or a with.
func branchNodes(node parse.Node) []parse.Node {
	switch n := node.(type) {
	case *parse.IfNode:
		return []parse.Node{n.Pipe, n.List, n.ElseList}
	case *parse.RangeNode:
		return []parse.Node{n.Pipe, n.List, n.ElseList}
	case *parse.WithNode:
		return []parse.Node{n.Pipe, n.List, n.ElseList}
	default:
		return nil
	}
}

// commandNodes converts the commands of a pipe to plain nodes.
func commandNodes(commands []*parse.CommandNode) []parse.Node {
	nodes := make([]parse.Node, 0, len(commands))
	for _, command := range commands {
		nodes = append(nodes, command)
	}

	return nodes
}

// parseTemplate parses prompt text into every tree it defines. Unknown functions are allowed: the test reads the tree and does not run it.
func parseTemplate(t *testing.T, text string) map[string]*parse.Tree {
	t.Helper()

	tree := parse.New("p")
	tree.Mode = parse.SkipFuncCheck
	trees := map[string]*parse.Tree{}

	_, err := tree.Parse(text, "{{", "}}", trees)
	require.NoError(t, err)

	return trees
}

// templateReadsBuildHost reports whether any tree that the text defines reads build_host.
func templateReadsBuildHost(t *testing.T, text string) bool {
	t.Helper()

	for _, tree := range parseTemplate(t, text) {
		if readsBuildHost(tree.Root) {
			return true
		}
	}

	return false
}

// The engine runs the gate and the mutation run, and the sandbox of an agent step denies the build host, so no prompt reads the build host fact.
// The test reads the parse tree, so every form that renders the host is refused: a field, an index, a with, a variable, a chain and a template call.
func TestPrompts_NoPromptReadsTheBuildHost(t *testing.T) {
	t.Parallel()

	forms := map[string]string{
		"field":    "Host: `{{ .facts.build_host }}`.",
		"if":       "{{ if .facts.build_host }}x{{ else }}y{{ end }}",
		"range":    "{{ range .facts.build_host }}x{{ end }}",
		"index":    `{{ index .facts "build_host" }}`,
		"with":     "{{ with .facts }}{{ .build_host }}{{ end }}",
		"variable": "{{ $f := .facts }}{{ $f.build_host }}",
		"chain":    "{{ (index . \"facts\").build_host }}",
		"template": `{{ define "h" }}{{ .facts.build_host }}{{ end }}{{ template "h" . }}`,
		"pipe":     `{{ index .facts "build_host" | printf "%s" }}`,
	}

	for name, form := range forms {
		require.True(t, templateReadsBuildHost(t, form), "the walk finds the %s form: %s", name, form)
	}

	for _, clean := range []string{"Repo `{{ .facts.repo }}`.", `{{ index .facts.vars "gate_files" }}`, "The build_host key is a fact."} {
		require.False(t, templateReadsBuildHost(t, clean), "the walk ignores %s", clean)
	}

	entries, err := fs.ReadDir(os.DirFS("prompts"), ".")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for _, entry := range entries {
		raw, err := fs.ReadFile(os.DirFS("prompts"), entry.Name())
		require.NoError(t, err)

		assert.False(t, templateReadsBuildHost(t, string(raw)), "prompts/%s reads the build host fact", entry.Name())
	}
}

// The sandbox of an agent step denies the build host, so no prompt orders a command there. The patterns are the host orders of the base prompts.
func TestPrompts_NoPromptOrdersACommandOnTheBuildHost(t *testing.T) {
	t.Parallel()

	orders := regexp.MustCompile(`(?i)on the build host|jobs folder of the host|check .df. on the build host`)

	for _, control := range []string{
		"Run every build, test and lint on the build host. Never run one on this machine.",
		"**Before EVERY build, check `df` on the build host.**",
		"Job scripts live in a jobs folder of the host (`jobs/<lane>/`).",
	} {
		require.True(t, orders.MatchString(control), "the pattern finds the order in %q", control)
	}

	entries, err := fs.ReadDir(os.DirFS("prompts"), ".")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for _, entry := range entries {
		raw, err := fs.ReadFile(os.DirFS("prompts"), entry.Name())
		require.NoError(t, err)

		assert.False(t, orders.MatchString(squash(string(raw))), "prompts/%s orders a command on the build host", entry.Name())
	}
}
