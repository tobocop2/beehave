package remediation_test

import (
	"io/fs"
	"os"
	"strings"
	"testing"

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
	integrate = "integrate"
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
		{"acceptance_rules", "stop and report it: a wider rule is the owner's decision", workLanes()},
		{"before_write_rules", "Check for an existing pull request, at the start and again before you push", []string{implement, fix, plan}},
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
		{"closing_rules", "Write the full report to the report file that the run names for this lane FIRST", everyLane()},
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
		{"gate_step_rules", "The gate steps come from the gate list of the round", workAndReview()},
		{"gate_step_rules", "prints `STEP_RESULT name=<name> rc=<n>`", workAndReview()},
		{"gate_step_rules", "A step you leave out prints `GATE-SKIP <name>: <reason>`", workAndReview()},
		{"gate_step_rules", "A missing tool is verified in the job", workAndReview()},
		{"gate_step_rules", "print `GATE-VOID <name>: pin <x>, ran <y>`", workAndReview()},
		{"gate_step_rules", "One failing test counts once", workAndReview()},
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
		{"build_host_rules", "Put no delete in any command you run", workAndReview()},
		{"build_host_rules", "never `sed -i.bak`", workAndReview()},
		{"build_host_rules", "Before EVERY build, check `df` on the build host", workAndReview()},
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
		{"review_rules", "List the gate steps that ran at the head you review", only},
		{"review_rules", "Condition 4, every item, unasked", only},
		{"review_rules", "MERGE-BASE content against head content", only},
		{"review_rules", "Plant an AWS key pair as a positive control", only},
	}
}

func gateEvidenceRules() []rule {
	return []rule{
		{"gate_evidence", "A red that the base tree has too is not counted", workAndReview()},
		{"gate_evidence", "A mutation run is its own job after the gate", workAndReview()},
		{"gate_evidence", "Capture the exit code before any pipe", workAndReview()},
		{"gate_evidence", "gives its stdin to every command inside it", workAndReview()},
		{"gate_evidence", "A regeneration is evidence for its own tree only", workAndReview()},
		{"gate_evidence", "becomes a full run in two cases", workAndReview()},
		{"gate_evidence", "assert the loop count against the input count", workAndReview()},
		{"gate_evidence", "search the diff for `TODO` and `FIXME`", workLanes()},
		{"gate_evidence", "names its author, as `TODO(name)`", workLanes()},
	}
}

func hostAndDiskRules() []rule {
	return []rule{
		{"host_disk_rules", "Disk is per lane", workAndReview()},
		{"host_disk_rules", "live in a jobs folder of the host", workAndReview()},
		{"host_disk_rules", "so that the orchestrator deletes it at once", workAndReview()},
		{"host_disk_rules", "Sweep dangling docker volumes as a backstop", workAndReview()},
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
		{"brief_rules", "On a round branch, the pull request body says `Addresses #N`", workLanes()},
		{"brief_rules", "When the base is the default branch, it says `Closes #N`", workLanes()},
		{"brief_rules", "Read an issue or pull request thread from the thread file", everyLane()},
		{"brief_rules", "A reproduction is self-contained", everyLane()},
	}
}

func mergeRules() []rule {
	return []rule{
		{"merge_rules", "Open a draft pull request at the first checkpoint", workLanes()},
		{"merge_rules", "Push every later commit to it at once", workLanes()},
		{"merge_rules", "goes into a native stack", workLanes()},
		{"merge_rules", "Link the branches from the bottom to the top with `gh stack link`", workLanes()},
		{"merge_rules", "Never use the merge queue of GitHub", []string{implement, fix, review, integrate}},
		{"merge_rules", "Known base reds are in one file", workAndReview()},
		{"merge_rules", "A finding that survives the review is filed before the merge", []string{review}},
		{"integrate_rules", "Add the integration row", []string{integrate}},
		{"integrate_rules", "Recount the heading and the title from the rows", []string{integrate}},
		{"integrate_rules", "Add a closing row for each issue that the merge resolves", []string{integrate}},
		{"integrate_rules", "Close each resolved issue with the evidence", []string{integrate}},
		{"merge_rules", "a commit that stays local is lost with the lane", workLanes()},
		{"merge_rules", "check that all of them show the same non-empty id", workLanes()},
		{"merge_rules", "the pull request body says `Addresses #N`", workLanes()},
		{"merge_rules", "When the base is the default branch, it says `Closes #N`", workLanes()},
		{"integrate_rules", "Never close an issue with no such comment", []string{integrate}},
		{"integrate_rules", "Closing keywords belong to the integration pull request only", []string{integrate}},
		{"integrate_rules", "A finding that survives is filed before the merge", []string{integrate}},
		{"integrate_rules", "fails when the heading or the title says another number than the rows, or when the row count did not change", []string{integrate}},
	}
}

func laneLayoutRules() []rule {
	return []rule{
		{"lane_layout", "A fix lane is not a proof lane and not a review lane", workLanes()},
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

// squash drops the bold marks and joins the words of a text with one space, so that a phrase matches across the line breaks and the bold spans of a prompt.
func squash(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "**", "")), " ")
}

func TestPrompts_HoldEveryRuleOfTheBriefText(t *testing.T) {
	t.Parallel()

	texts := map[string]string{}
	folder := os.DirFS("prompts")

	for _, name := range append(everyLane(), integrate) {
		raw, err := fs.ReadFile(folder, name+".md")
		require.NoError(t, err)

		texts[name] = squash(string(raw))
	}

	groups := [][]rule{acceptanceRules(), reportRules(), evidenceRules(), designRules(), shellRules(), compiledAndUnionRules(), reviewRules(), gateEvidenceRules(), hostAndDiskRules(), reviewFormatRules(), briefRules(), mergeRules(), laneLayoutRules()}

	held, counted := 0, 0

	for _, group := range groups {
		counted += len(group)

		for _, lost := range group {
			for _, name := range lost.prompts {
				held++

				// A plain check: a failed assert.Contains would print the whole prompt.
				if !strings.Contains(texts[name], lost.phrase) {
					t.Errorf("prompts/%s.md lost the phrase %q of the rule in %s", name, lost.phrase, lost.from)
				}
			}
		}
	}

	// One phrase for each mapped or added row of the proof table that has a phrase of its own: a dropped phrase fails here.
	assert.Equal(t, 153, counted)
	assert.Positive(t, held)
}
