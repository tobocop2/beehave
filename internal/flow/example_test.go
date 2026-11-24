package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/config"
	gatepkg "github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

func mustRead(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // the tests name their own example files
	require.NoError(t, err)

	return data
}

const reviewMarker = "What every review here must do"

// fullReview is the phrase of the prompt of a review of the whole pull request: the full review of a candidate, not a scoped review.
const fullReview = "The scope of this review: the whole pull request"

// exampleProject is the project facts that the example rig starts with: the repository, the trunk and the settings of a round on the branch integration.
func exampleProject() model.Project {
	return model.Project{
		Name: testProject, Repo: "o/r", BaseBranch: "main",
		Vars: with(roundVars(map[string]any{
			"base": "integration", "owner": "owner", "owner_email": "owner@example.com", "source_checkout": "/own/checkout",
			"lane_cap": json.Number("3"), "fix_rounds": json.Number("5"), "label": "beehave-round", "steps": exampleSteps(),
		}), map[string]any{"review_skill": exampleSkill}),
	}
}

// exampleSteps is vars.round.steps of the example rig: the steps of the review loop on the models that the model-order assertions of the tests name.
func exampleSteps() map[string]any {
	model := func(name string) any { return map[string]any{"model": name} }

	return map[string]any{
		"implement": model("sonnet"), "review": model("opus"), "fix": model("sonnet"), "fix_late": model("opus"), "design": model("opus"),
	}
}

// setRound sets the round setting `key` of the project of the rig before the run starts. A nil value removes the key.
// oneFixRound is the smallest cap of the round: two candidates. A test that only needs the ruling of the owner after the cap sets it,
// so a review that always asks for changes reaches the owner after 13 agent calls of the loop (capModels(2)) and not after 41.
var oneFixRound = json.Number("1")

func (rig *agentRig) setRound(key string, value any) {
	setRoundOf(&rig.project, key, value)
}

// setRoundOf sets the round setting `key` in the vars of the project. A nil value removes the key. Project vars with no round get one.
func setRoundOf(project *model.Project, key string, value any) {
	settings, found := project.Vars["round"].(map[string]any)
	if !found {
		settings = map[string]any{}
	}

	project.Vars["round"] = with(settings, map[string]any{key: value})
}

// runPipeline runs the item of the rig as the child of a round: the entry pipeline of the file, the way spawn starts it. It runs the file as it is.
// The child never merges, so the result holds the value that pipeline returned and no merge state.
func runPipeline(engine *Engine, file string) (ItemResult, error) {
	return runPipelineOf(engine, testItem, file)
}

// runPipelineOf is runPipeline for the item `item`.
func runPipelineOf(engine *Engine, item, file string) (ItemResult, error) {
	in, err := pinnedInput(item, testProject, file, modeOff())
	if err != nil {
		return ItemResult{}, err
	}

	in.Entry = "pipeline"

	return engine.run(in)
}

// childValue is what the entry pipeline returned: the item, the head the gate ran on, whether the review approved it and the reason when it did not.
type childValue struct {
	Item     string `json:"item"`
	Head     string `json:"head"`
	Approved bool   `json:"approved"`
	Reason   string `json:"reason"`
}

// childOf reads the value that the entry pipeline returned.
func childOf(t *testing.T, result ItemResult) childValue {
	t.Helper()

	var value childValue

	require.NoError(t, json.Unmarshal(result.Value, &value))

	return value
}

// exampleRig runs examples/remediation/workflow.star, copied with its prompts, over the fake collaborators.
// A review whose verdict is the empty string writes no output file.
func exampleRig(t *testing.T, verdicts func(review int) string) (*agentRig, string) {
	t.Helper()

	return exampleRigWithFindings(t, verdicts, func(int) string { return "[]" })
}

// exampleTree is the tree that the gate of the example rig records for every head.
const exampleTree = "tree-of-the-gated-head"

// exampleRigWithFindings is exampleRig with the findings (a JSON list) that each review writes beside its verdict.
func exampleRigWithFindings(t *testing.T, verdicts, findings func(review int) string) (*agentRig, string) {
	t.Helper()

	return exampleRigLimited(t, verdicts, findings, func(int) string { return "" }, "", capReviews)
}

// capReviews is the most reviews of a run of the example at its cap: 6 candidates, each with 3 scoped reviews and a full review.
const capReviews = 6 * 4

// designMarker is a phrase of the prompt of the design step of the example.
const designMarker = "What a design step must do"

// designReviewLimit is the most reviews that any design test expects. A run at the cap has capReviews reviews, so a loop that fails to stop fails at the eighth review.
const designReviewLimit = 7

// exampleRigWithDesign is exampleRigWithFindings with the mechanism that each review names, and the JSON object that the design step writes.
// A nil mechanisms leaves the output out of the review.
func exampleRigWithDesign(t *testing.T, verdicts, findings, mechanisms func(review int) string, design string) (*agentRig, string) {
	t.Helper()

	return exampleRigLimited(t, verdicts, findings, mechanisms, design, designReviewLimit)
}

// exampleRigLimited is exampleRigWithDesign with the most reviews that the test expects.
func exampleRigLimited(t *testing.T, verdicts, findings, mechanisms func(review int) string, design string, limit int) (*agentRig, string) {
	t.Helper()

	folder := t.TempDir()
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", "remediation"))))

	skill := exampleSkill

	rig := newAgentRig(t)
	rig.h.mutation = exampleMutationCommand
	rig.h.slots = func(_, _, class string) (SlotGrant, error) {
		return SlotGrant{Host: "box", Count: map[string]int{"mutation": 1, model.DefaultClass: 1}[class]}, nil
	}
	rig.project = exampleProject()
	rig.h.facts = func(string) model.Project { return rig.project }
	rig.h.reader = &fakeReader{}
	rig.h.preflight = &fakePreflight{}
	rig.h.gate.results = []GateResult{{Passed: true, Head: rig.head, Tree: exampleTree}}
	rig.h.gate.checks = cleanChecks()
	rig.baseAt(t, "integration")
	rig.h.forge.state = review.Standing{Number: testPull, Head: rig.head}

	rig.answerLikeTheExample(skill, verdicts, findings, mechanisms, design, limit)

	return rig, filepath.Join(folder, "workflow.star")
}

// baseAt makes the remote branch of the base point at the head of the rig, so the checks before review find a head that descends from the base.
func (rig *agentRig) baseAt(t *testing.T, base string) {
	t.Helper()

	gitIn(t, rig.dir, "update-ref", "refs/remotes/origin/"+base, rig.head)
}

// answerLikeTheExample makes the fake agents of the rig answer the prompts of the example: a review writes the verdict, the findings and the mechanism of the review it is,
// a plan or a review writes the regression count, a design step writes the design, and every run reports the skill that the implement step requires.
func (rig *agentRig) answerLikeTheExample(skill string, verdicts, findings, mechanisms func(review int) string, design string, limit int) {
	// The children of a round run their agents at the same time, so the count is atomic.
	var count atomic.Int64

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		if strings.Contains(request.Prompt, reviewMarker) {
			reviews := int(count.Add(1))

			// A loop that does not stop where the test expects would run to the rounds cap and then wait for a person. Past the limit the
			// review writes no output, so the run fails at once and the test reports it as an assertion, not as a timeout.
			path := promptOutput(request.Prompt)
			if verdict := verdicts(reviews); verdict != "" && reviews <= limit {
				outputs := map[string]string{"verdict": verdict, "regressions": "0", "open": openOf(findings(reviews)), "findings": findings(reviews)}
				if mechanisms != nil {
					outputs["mechanism"] = mechanisms(reviews)
				}

				rig.putOutputs(path, outputs)
			}
		}

		if !strings.Contains(request.Prompt, reviewMarker) {
			rig.putDeclared(request)
		}

		if strings.Contains(request.Prompt, designMarker) {
			rig.put(promptOutput(request.Prompt), design)
		}

		return agent.Result{Outcome: agent.OutcomeExited, Session: "sess-1", Transcript: "/t.jsonl", Text: "all done", Skills: []string{skill}}
	}
}

// promptOutput returns the path of the output file that a prompt of the example names: the text between "Write it to " and " as JSON".
func promptOutput(prompt string) string {
	_, rest, _ := strings.Cut(prompt, "Write it to ")
	path, _, _ := strings.Cut(rest, " as JSON")

	return path
}

// cleanLaneReport is the report that a lane of the example rig writes: the four tables and the background line. The kind is the lane that wrote it, so a test tells the reports apart.
func cleanLaneReport(kind string) string {
	return "# Report of the " + kind + " lane\n\n## Claims\n| Claim | Where | Evidence | Verified at |\n|---|---|---|---|\n| none: " + kind + " | none | none | none |\n\n" +
		"## Hunks\n| Hunk | Arm | Red test line |\n|---|---|---|\n| none: " + kind + " | none | none |\n\n## Inputs\n| Input class | Base | Head | Test |\n|---|---|---|---|\n| none: " + kind + " | none | none | none |\n\n" +
		"## Siblings\n| Site | Same behavior | Updated |\n|---|---|---|\n| none: " + kind + " | none | none |\n\nno background commands left\n"
}

// laneKind names the lane that a request of the example rig starts, by a phrase of its prompt.
func laneKind(request AgentRequest) string {
	if strings.Contains(request.Prompt, fixMarker) {
		return "fix"
	}

	return "implement"
}

// putDeclared makes a request of the example rig that is no review answer each output it declares with a clean value: a count of none, no parked
// finding, no red that the lane did not cause and a whole report. The outputs of a review and of the design step are written by their own rules.
// The file holds no value for any other name, so the engine runs the step again on the missing output while output_tries allows,
// then fails the run, until this table knows it.
func (rig *agentRig) putDeclared(request AgentRequest) {
	clean := map[string]string{"regressions": "0", "parked": "[]", "uncaused": "[]", "resolved": "[]", "wontfix": "[]", "report": cleanLaneReport(laneKind(request))}
	answer := map[string]string{}

	for _, name := range request.Outputs {
		value, known := clean[name]

		if known {
			answer[name] = value
		}
	}

	if len(answer) == 0 {
		return
	}

	rig.putOutputs(request.OutputFile, answer)
}

// putOutputs writes the outputs of a fake agent as the JSON object that the agent step reads. An encoding error is kept like a failed write, and run reports it.
func (rig *agentRig) putOutputs(path string, outputs map[string]string) {
	text, err := json.Marshal(outputs)

	rig.mu.Lock()
	rig.failed = append(rig.failed, err)
	rig.mu.Unlock()

	rig.put(path, string(text))
}

// exampleSkill is the skill that the project of the example rig names in vars.review_skill, and that the fake agent reports.
const exampleSkill = "the-review-skill"

// openOf is the output open that a review writes for a list of findings: empty for none, else a line.
func openOf(findings string) string {
	if findings == "[]" {
		return ""
	}

	return "an open finding"
}

// models returns the model of each agent call of the run in order, except the sweep step, which runs on the model of the implement step right after it.
// The tests of the sweep read its request from requests.
func (r *agentRig) models() []string {
	var models []string

	for _, request := range r.h.agents.requests {
		if !isSweep(request) {
			models = append(models, request.Model)
		}
	}

	return models
}

// capRig makes every agent call that matches capped end at a cap.
func capRig(t *testing.T, verdicts func(review int) string, capped func(AgentRequest) bool, outcome agent.Outcome) (*agentRig, string) {
	t.Helper()

	rig, file := exampleRig(t, verdicts)
	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)
		if capped(request) {
			result.Outcome = outcome
		}

		return result
	}

	return rig, file
}

func TestExample_ACappedImplementStopsTheItemWithARefuseThatNamesTheCapAndStartsNoGateOrReview(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(int) string { return "approve" }, func(r AgentRequest) bool { return r.Model == "sonnet" && r.Resume == "" }, agent.OutcomeTurnCap)

	_, err := runPipeline(rig.h.engine(t), file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "turns")
	assert.Equal(t, []string{"opus", "sonnet"}, rig.models(), "no review starts after the capped implement")
	assert.Zero(t, rig.h.gate.calls, "no gate runs after the capped implement")
}

func TestExample_ACappedFixStopsTheItemWithARefuseThatNamesTheCap(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	}, func(r AgentRequest) bool { return r.Resume != "" && !isSweep(r) }, agent.OutcomeBudgetCap)

	_, err := runPipeline(rig.h.engine(t), file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "budget")
}

func TestExample_AnApprovedFirstReviewMarksTheHeadReadyWithoutAFix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "opus"}, rig.models(), "the scoped review approves the implement, so the candidate gets its full review")
	assert.True(t, childOf(t, result).Approved, "the child returns the approved head for the round to land")
}

func TestExample_AnApproveWithAnOpenFindingIsAChangesVerdictAndSendsTheRunToAFix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(int) string { return "approve" }, func(review int) string {
		if review == 1 {
			return `[{"id":"","severity":"minor","path":"a.go","text":"rename x"}]`
		}

		return "[]"
	})

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "opus"}, rig.models(), "the follow-up of scoped review 1 was fixed, scoped review 2 approved, and the full review approved")
	assert.True(t, childOf(t, result).Approved, "the child returns the approved head for the round to land")
}

// libraryLoop is a workflow that runs the review loop of the library on the base integration, with the implementer at head and the extra arguments.
func libraryLoop(head, extra string) string {
	return "load(\"@beehave//review_fix.star\", \"review_fix\")\n" +
		"def main(item):\n" +
		"    impl = struct(violations = \"\", session = \"\", rewrote = False, head_sha = \"" + head + "\")\n" +
		"    review_fix(item, impl, \"prompts/review.md\", \"prompts/fix.md\", base = \"integration\", facts = facts(), steps = facts().vars[\"round\"][\"steps\"]" + extra + ")\n"
}

// The fixes after candidate k are fix round k, as the fix after review k was before the scoped loop. Rounds 1 to 3 resume the implementer on sonnet;
// round 4 starts fresh on opus. Candidate 1 has two scoped fixes after a clean implementer, and they are round 1 too.
func TestReviewFix_FixRoundThreeResumesTheImplementerOnSonnetAndRoundFourStartsFreshOnOpus(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	loop := strings.Replace(libraryLoop(rig.head, ", rounds = 5"), `session = ""`, `session = "implement-session"`, 1)
	require.NoError(t, os.WriteFile(file, []byte(loop), 0o600))

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file, modeOff())
	require.NoError(t, err)

	tiers := map[string][]string{}

	for _, request := range rig.h.agents.requests {
		for _, round := range []string{"1", "2", "3", "4", "5"} {
			if strings.Contains(request.Prompt, "This is round "+round+".") {
				tiers[round] = append(tiers[round], request.Model+" "+request.Resume)
			}
		}
	}

	resumed, fresh := "sonnet implement-session", "opus "
	assert.Equal(t, map[string][]string{
		"1": {resumed, resumed, resumed, resumed, resumed},
		"2": {resumed, resumed, resumed},
		"3": {resumed, resumed, resumed},
		"4": {fresh, fresh, fresh},
	}, tiers, "five candidates: fix rounds 1 to 3 resume on sonnet, fix round 4 starts fresh on opus")
}

// With a clean implementer no fix runs before the first scoped review, so that review judges the diff of the implementer, not the diff of a fix.
func TestReviewFix_TheFirstScopedReviewOfACleanImplementerJudgesTheDiffOfTheImplementer(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	require.NoError(t, os.WriteFile(file, []byte(libraryLoop(rig.head, "")), 0o600))

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file, modeOff())
	require.NoError(t, err)

	require.Len(t, rig.h.agents.requests, 2, "the scoped review and the full review of the one candidate")
	assert.Contains(t, rig.h.agents.requests[0].Prompt, "The scope of this review: the diff of the implementer.")
	assert.Contains(t, rig.h.agents.requests[1].Prompt, fullReview)
}

// The last scoped review of a candidate that changed the folder is a violation of that candidate: the full review that follows cannot approve it clean.
func TestReviewFix_AViolationOfTheLastScopedReviewKeepsTheFullReviewFromACleanApprove(t *testing.T) {
	t.Parallel()

	// MaxInnerReviews is MAX_INNER of the library: the scoped reviews of one candidate.
	const MaxInnerReviews = 3

	rig, file := exampleRig(t, func(review int) string {
		if review <= MaxInnerReviews {
			return "changes"
		}

		return "approve"
	})
	require.NoError(t, os.WriteFile(file, []byte(libraryLoop(rig.head, ", rounds = 2")), 0o600))

	inner := rig.h.agents.result
	reviews := 0

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)

		if !strings.Contains(request.Prompt, reviewMarker) {
			return result
		}

		reviews++

		// The last scoped review writes a tracked file. The full review leaves the folder clean again, so only the violation of the scoped review is open.
		switch reviews {
		case MaxInnerReviews:
			rig.put(filepath.Join(rig.dir, "edited.txt"), "a shell write of the last scoped review")
			gitIn(t, rig.dir, "add", "edited.txt")
		case MaxInnerReviews + 1:
			gitIn(t, rig.dir, "rm", "-qf", "edited.txt")
		}

		return result
	}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file, modeOff())
	require.NoError(t, err)

	// Candidate 1: the scoped review of the implementer, two scoped fixes and reviews, the full review. Its approve is not clean, so candidate 2
	// starts with a fix, its scoped review and its full review.
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "opus", "sonnet", "opus", "opus"}, rig.models())
	assert.Equal(t, 2, countCandidates(rig))
}

// The third scoped review asks for changes, so the head goes to the full checks with its findings open. A red gate sends the head back, and the next
// fix gets the report of the gate and the open findings of that review.
func TestReviewFix_TheFindingsThatTheLastScopedReviewLeftOpenGoToTheFixAfterARedGate(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(review int) string {
		if review <= 3 {
			return "changes"
		}

		return "approve"
	})
	rig.h.gate.results = []GateResult{{Passed: false, Head: rig.head, Report: "lint: unused variable x"}, {Passed: true, Head: rig.head, Tree: exampleTree}}
	require.NoError(t, os.WriteFile(file, []byte(libraryLoop(rig.head, ", rounds = 2")), 0o600))

	inner := rig.h.agents.result
	reviews := 0

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)

		if strings.Contains(request.Prompt, reviewMarker) {
			reviews++

			if reviews == 3 {
				result.Text = "the open finding of the third scoped review"
			}
		}

		return result
	}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file, modeOff())
	require.NoError(t, err)

	// The scoped review of the implementer, two scoped fixes and reviews, the red gate, then the fix of candidate 2, its scoped review and its full review.
	require.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus", "opus"}, rig.models())

	fix := rig.h.agents.requests[5].Prompt
	assert.Contains(t, fix, "lint: unused variable x", "the report of the red gate")
	assert.Contains(t, fix, "the open finding of the third scoped review", "the findings that the last scoped review left open")
}

func TestReviewFix_TheFollowUpsArgumentReachesTheReviewAndTheDefaultAllowsThem(t *testing.T) {
	t.Parallel()

	for extra, want := range map[string][]string{
		"":                        {"opus", "opus"},
		`, follow_ups = "allow"`:  {"opus", "opus"},
		`, follow_ups = "forbid"`: {"opus", "sonnet", "opus", "opus"},
	} {
		rig, file := exampleRigWithFindings(t, func(int) string { return "approve" }, func(review int) string {
			if review == 1 {
				return `[{"id":"","severity":"deferred","path":"a.go","text":"later"}]`
			}

			return "[]"
		})
		require.NoError(t, os.WriteFile(file, []byte(libraryLoop(rig.head, extra)), 0o600))

		loopEngine := rig.h.engine(t)
		_, err := loopEngine.RunFile(testItem, testProject, file, modeOff())

		require.NoError(t, err, extra)
		assert.Equal(t, want, rig.models(), extra)
	}
}

func TestReviewFix_TheStatusShowsTheLabelOfTheLastReviewWithItsRoundAgainstTheCapOfTheLoop(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(int) string { return "approve" }, func(review int) string {
		if review == 1 {
			return `[{"id":"","severity":"deferred","path":"a.go","text":"later"}]`
		}

		return "[]"
	})
	require.NoError(t, os.WriteFile(file, []byte(libraryLoop(rig.head, `, follow_ups = "forbid", rounds = 4`)), 0o600))

	loopEngine := rig.h.engine(t)
	_, err := loopEngine.RunFile(testItem, testProject, file, modeOff())
	require.NoError(t, err)

	info := onlyRun(t, rig.h.client(t, testVersion))

	assert.Equal(t, "review 3/16", info.Label, "two scoped reviews and the full review; each candidate has at most 3 scoped reviews and a full review")
}

// capModels is the models of the loop of a run of the example whose every review asks for changes, up to a cap of the given candidates.
// Candidate 1 starts at a scoped review of the implement; each later candidate starts at a scoped fix. The scoped loop of a candidate ends after
// its third scoped review, and the full review follows. The fixes toward candidate k are fix round k-1, so the fixes of candidates 5 and later
// (fix rounds 4 and 5) run on opus.
func capModels(candidates int) []string {
	models := []string{"opus", "sonnet", "opus", "sonnet", "opus", "opus"}
	for candidate := 2; candidate <= candidates; candidate++ {
		fix := "sonnet"
		if candidate > 4 {
			fix = "opus"
		}

		models = append(models, fix, "opus", fix, "opus", fix, "opus", "opus")
	}

	return models
}

func TestExample_SixCandidatesAreTheCapAndTheSixthFullReviewIsTheLast(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	engine := rig.h.engine(t)
	done := pipelineInBackground(engine, file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	outcome := receive(t, done)
	require.NoError(t, outcome.err)

	// plan, implement, the six candidates, and the settle agent.
	assert.Equal(t, slices.Concat([]string{"opus", "sonnet"}, capModels(6), []string{"sonnet"}), rig.models())
	assert.Equal(t, 6, rig.h.gate.calls, "one gate for each candidate")
}

func TestExample_AGateThatCannotRunEndsTheRunBeforeTheFullReviewOrAnyFix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.gate.err = errFake

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorIs(t, err, errFake)
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models(), "only the plan, the implement and the scoped review ran: the gate comes after the scoped review")
	assert.Equal(t, 1, rig.h.gate.calls, "no second round")
}

// packageGate runs the real gate package on a local project whose gate command is killed by a signal on the first run and exits 0 on the next.
type packageGate struct {
	head, tree string
	runs       *int
}

func (packageGate) Submit(context.Context, string, string, string, string, string, string, model.Slot) (GateJob, error) {
	return GateJob{}, nil
}

func (g packageGate) Run(ctx context.Context, _, _, _ string, _ GateJob) (GateResult, error) {
	*g.runs++

	code := 0
	if *g.runs == 1 {
		code = 128 + 15
	}

	shell := func(context.Context, string, string) (string, int) { return "partial output", code }
	tree := func(context.Context, string) (gatepkg.State, error) {
		return gatepkg.State{Head: g.head, Tree: g.tree, Clean: true}, nil
	}

	result, err := gatepkg.New(shell, tree, nil).Run(ctx, gatepkg.Request{Project: model.Project{Name: "demo", Gate: "make check", BuildHost: config.LocalHost}, Dir: "/w"})
	if err != nil {
		return GateResult{}, err
	}

	steps := make([]GateStep, 0, len(result.Steps))
	for _, s := range result.Steps {
		steps = append(steps, GateStep{Name: s.Name, Status: string(s.Status)})
	}

	return GateResult{Passed: result.Passed, Report: result.Report, Head: result.Head, Tree: result.Tree, Steps: steps}, nil
}

// diedThenDoneHost is a build host whose first job died and whose later jobs succeed.
type diedThenDoneHost struct{ waits *int }

func (diedThenDoneHost) Submit(context.Context, model.JobSpec) (model.JobID, error) {
	return "job-1", nil
}

func (h diedThenDoneHost) Wait(context.Context, model.JobID) (model.JobState, error) {
	*h.waits++

	if *h.waits == 1 {
		return model.JobDied, nil
	}

	return model.JobSucceeded, nil
}

func (diedThenDoneHost) Cancel(context.Context, model.JobID) error { return nil }

func (diedThenDoneHost) WorkRoot(context.Context) (string, error) { return "/home/ob", nil }

// hostDiedGate runs the real gate package on a project with a build host. The job of the first run died; the job of the next run succeeds.
type hostDiedGate struct {
	head, tree string
	waits      *int
}

func (hostDiedGate) Submit(context.Context, string, string, string, string, string, string, model.Slot) (GateJob, error) {
	return GateJob{}, nil
}

func (g hostDiedGate) Run(ctx context.Context, _, _, _ string, _ GateJob) (GateResult, error) {
	tree := func(context.Context, string) (gatepkg.State, error) {
		return gatepkg.State{Head: g.head, Tree: g.tree, Clean: true}, nil
	}
	hosts := func(model.Project) (gatepkg.Remote, error) { return diedThenDoneHost{waits: g.waits}, nil }
	project := model.Project{Name: "demo", Gate: "make check", BuildHost: "box"}

	result, err := gatepkg.New(nil, tree, hosts).Run(ctx, gatepkg.Request{Project: project, Dir: "/w", CloneURL: "https://example.test/demo.git"})
	if err != nil {
		return GateResult{}, err
	}

	steps := make([]GateStep, 0, len(result.Steps))
	for _, s := range result.Steps {
		steps = append(steps, GateStep{Name: s.Name, Status: string(s.Status)})
	}

	return GateResult{Passed: result.Passed, Report: result.Report, Head: result.Head, Tree: result.Tree, Steps: steps}, nil
}

// zeroTestGate runs the real gate package on a local project that sets test_report. The gate command exits 0 and writes a report with no test.
// The first run writes a report with 0 tests; every later run writes one with 3.
type zeroTestGate struct {
	head, tree, dir string
	runs            *int
}

func (zeroTestGate) Submit(context.Context, string, string, string, string, string, string, model.Slot) (GateJob, error) {
	return GateJob{}, nil
}

func (g zeroTestGate) Run(ctx context.Context, _, _, _ string, _ GateJob) (GateResult, error) {
	*g.runs++
	runs := *g.runs

	shell := func(context.Context, string, string) (string, int) {
		report := `<testsuites><testsuite tests="0"/></testsuites>`
		if runs > 1 {
			report = `<testsuites><testsuite tests="3"/></testsuites>`
		}

		if err := os.WriteFile(filepath.Join(g.dir, "report.xml"), []byte(report), 0o600); err != nil {
			return err.Error(), 1
		}

		return "ok", 0
	}
	tree := func(context.Context, string) (gatepkg.State, error) {
		return gatepkg.State{Head: g.head, Tree: g.tree, Clean: true}, nil
	}
	project := model.Project{Name: "demo", Gate: "go test ./nothing", TestReport: "report.xml", BuildHost: config.LocalHost}

	result, err := gatepkg.New(shell, tree, nil).Run(ctx, gatepkg.Request{Project: project, Dir: g.dir})
	if err != nil {
		return GateResult{}, err
	}

	return GateResult{Passed: result.Passed, Report: result.Report, Head: result.Head, Tree: result.Tree, Steps: commandStep(result.Passed)}, nil
}

func TestExample_AGateThatExitsZeroWithZeroTestsDoesNotPassAndTheFixGetsTheReason(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	runs := 0
	rig.h.gate.via = zeroTestGate{head: rig.head, tree: exampleTree, dir: t.TempDir(), runs: &runs}

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "opus"}, rig.models(), "the gate of candidate 1 did not pass: a fix and a scoped review make candidate 2, and only it gets the full review")
	assert.Contains(t, rig.h.agents.requests[4].Prompt, "hold 0 tests", "the reason reaches the fix")
	assert.Equal(t, 2, runs)
}

func TestExample_AGateKilledByASignalIsVoidAndRerunWithNoFixAgent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	runs := 0
	rig.h.gate.via = packageGate{head: rig.head, tree: exampleTree, runs: &runs}

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "opus"}, rig.models(), "a killed gate is void, not red: no fix agent, and the full review waits for the rerun")
	assert.Equal(t, 2, runs, "the rerun is a new gate step")
}

func TestExample_AHostGateThatDiedIsVoidAndRerunWithNoFixAgent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	waits := 0
	rig.h.gate.via = hostDiedGate{head: rig.head, tree: exampleTree, waits: &waits}

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "opus"}, rig.models(), "a died job is void, not red: no fix agent, and the full review waits for the rerun")
	assert.Equal(t, 2, waits, "the rerun is a new gate step")
}

func voidResult(rig *agentRig, report string) GateResult {
	return GateResult{Passed: false, Head: rig.head, Tree: exampleTree, Report: report, Steps: stepsUnknown}
}

func TestExample_AVoidGateIsRerunInTheRunAndAPassAfterItGivesAPassWithNoFixAgent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.gate.results = []GateResult{voidResult(rig, "the gate wrote no report"), {Passed: true, Head: rig.head, Tree: exampleTree}}

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "opus"}, rig.models(), "a void gate is not red: the implementer and both reviews run, and no fix agent")
	assert.Equal(t, 2, rig.h.gate.calls, "the rerun is a new gate step")
}

func TestExample_AGateThatIsVoidEveryTimeAsksTheOwnerAndRefusesAfterTheLastAnswerWithNoFixRound(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.gate.results = []GateResult{voidResult(rig, "")}
	client := rig.h.client(t, testVersion)
	done := make(chan runOutcome, 1)
	engine := rig.h.engine(t)

	go func() {
		result, err := runPipeline(engine, file)
		done <- runOutcome{result: result, err: err}
	}()

	for range 3 {
		waitOn(t, waiting[struct{}]{what: "the run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
			return struct{}{}, client.Reply(testItem, Reply{Text: "run it again"}) == nil, nil
		}})
	}

	outcome := receive(t, done)

	require.ErrorContains(t, outcome.err, "the gate gave no result")
	require.ErrorContains(t, outcome.err, "tests", "the refusal names the step with no result, also when the report is empty")
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models(), "no fix agent and no full review start after the scoped review")
	assert.Equal(t, 6, rig.h.gate.calls, "the gate, two reruns, and one gate after each of the three answers")
}

func TestExample_AnAnswerToAVoidGateQuestionRunsTheGateAgainAndAPassGoesToTheReview(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.gate.results = []GateResult{voidResult(rig, "x"), voidResult(rig, "x"), voidResult(rig, "x"), {Passed: true, Head: rig.head, Tree: exampleTree}}
	client := rig.h.client(t, testVersion)
	done := make(chan runOutcome, 1)
	engine := rig.h.engine(t)

	go func() {
		result, err := runPipeline(engine, file)
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "run it again"}) == nil, nil
	}})

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, 4, rig.h.gate.calls, "the answer, which the refusal-free question names, ran the gate again")
	assert.Equal(t, []string{"opus", "sonnet", "opus", "opus"}, rig.models(), "the scoped review, then the full review of the passed gate")
}

func TestExample_AGateWithAFailedStepAndAnUnknownStepIsRedAndStillGetsAFixRound(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	steps := []GateStep{{Name: "command", Status: "fail"}, {Name: "tests", Status: "unknown"}}
	rig.h.gate.results = []GateResult{{Passed: false, Head: rig.head, Tree: exampleTree, Report: "red", Steps: steps}}

	client := rig.h.client(t, testVersion)
	engine := rig.h.engine(t)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := runPipeline(engine, file)
		done <- runOutcome{result: result, err: err}
	}()

	// The red gate repeats until the rounds cap, and then the run waits for a person for the whole claim lease. The test ends it after
	// the second gate, which is the claim of the test.
	waitOn(t, waiting[struct{}]{what: "the second gate call", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		rig.h.gate.mu.Lock()
		defer rig.h.gate.mu.Unlock()

		return struct{}{}, rig.h.gate.calls > 1, nil
	}})
	require.NoError(t, client.Cancel(testItem))
	rig.h.tick(t)

	err := receive(t, done).err

	assert.NotContains(t, fmt.Sprint(err), "the gate gave no result")
}

func TestExample_AReviewIsOnlyReadAgainstTheHeadTheGatePassed(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	// The gate reports a commit on top of the base that the worktree does not hold, so the checks before review pass and only the pin of the review differs.
	other := gitIn(t, rig.dir, "commit-tree", "-p", rig.head, "-m", "not the worktree", rig.head+"^{tree}")
	rig.h.gate.results = []GateResult{{Passed: true, Head: other, Tree: exampleTree}}

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, other)
	require.ErrorContains(t, err, rig.head)
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models(), "the full review agent never started; the scoped review was pinned to the head of the worktree")
}

func TestExample_AReviewThatWritesNoVerdictFileIsAskedOnceMore(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(review int) string {
		if review == 1 {
			return ""
		}

		return "approve"
	})

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "opus", "opus"}, rig.models(), "the scoped review was asked once more, and the full review followed")
	assert.True(t, childOf(t, result).Approved, "the child returns the approved head for the round to land")
}

func TestExample_AFailedGateSkipsTheReviewAndSendsItsReportToTheFix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.gate.results = []GateResult{{Passed: false, Head: rig.head, Report: "lint: unused variable x"}, {Passed: true, Head: rig.head, Tree: exampleTree}}

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "opus"}, rig.models(), "candidate 1 is red and gets no full review; the fix and a scoped review make candidate 2, which gets it")

	fix := rig.h.agents.requests[4].Prompt
	assert.Contains(t, fix, "lint: unused variable x", "the gate report is the findings of the fix")
}

// runInBackground2 runs the entry main of the file in the background.
func runInBackground2(engine *Engine, file string) <-chan runOutcome {
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	return done
}

// pipelineInBackground runs the entry pipeline of the example file in the background: the work on one item, which main of the example starts as a child of its round.
func pipelineInBackground(engine *Engine, file string) <-chan runOutcome {
	done := make(chan runOutcome, 1)

	go func() {
		result, err := runPipeline(engine, file)
		done <- runOutcome{result: result, err: err}
	}()

	return done
}
