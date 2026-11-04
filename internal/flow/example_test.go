package flow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/config"
	gatepkg "github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

func mustRead(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // the tests name their own example files
	require.NoError(t, err)

	return data
}

const reviewMarker = "What every review here must do"

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

	return exampleRigLimited(t, verdicts, findings, func(int) string { return "" }, "", 6)
}

// designMarker is a phrase of the prompt of the design step of the example.
const designMarker = "What a design step must do"

// designReviewLimit is the most reviews that any design test expects. The rounds cap is 6, so a loop that fails to stop fails at the sixth review.
const designReviewLimit = 5

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

	skill := exampleSkill(t, folder)

	rig := newAgentRig(t)
	rig.h.opts.Env = map[string]string{"REPO": "o/r", "BASE": "integration", "OWNER_LOGIN": "owner", "OWNER_EMAIL": "owner@example.com", "SOURCE_CHECKOUT": "/own/checkout", "MERGE": "false", "EXPECT_HEAD": ""}
	rig.h.preflight = &fakePreflight{}
	rig.h.gate.results = []GateResult{{Passed: true, Head: rig.head, Tree: exampleTree}}
	rig.h.forge.state = review.Standing{Number: testPull, Head: rig.head}

	reviews := 0

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		if strings.Contains(request.Prompt, reviewMarker) {
			reviews++

			// A loop that does not stop where the test expects would run to the rounds cap and then wait for a person. Past the limit the
			// review writes no output, so the run fails at once and the test reports it as an assertion, not as a timeout.
			_, rest, _ := strings.Cut(request.Prompt, "Write it to ")
			path, _, _ := strings.Cut(rest, " as JSON")
			if verdict := verdicts(reviews); verdict != "" && reviews <= limit {
				mechanism := ""
				if mechanisms != nil {
					mechanism = `"mechanism":"` + mechanisms(reviews) + `",`
				}

				rig.put(path, `{"verdict":"`+verdict+`",`+mechanism+`"regressions":"0","findings":`+findings(reviews)+`}`)
			}
		}

		if !strings.Contains(request.Prompt, reviewMarker) && slices.Contains(request.Outputs, "regressions") {
			rig.put(request.OutputFile, `{"regressions":"0"}`)
		}

		putNoParked(rig, request)

		if strings.Contains(request.Prompt, designMarker) {
			_, rest, _ := strings.Cut(request.Prompt, "Write it to ")
			path, _, _ := strings.Cut(rest, " as JSON")
			rig.put(path, design)
		}

		return agent.Result{Outcome: agent.OutcomeExited, Session: "sess-1", Transcript: "/t.jsonl", Text: "all done", Skills: []string{skill}}
	}

	return rig, filepath.Join(folder, "workflow.star")
}

// putNoParked makes the settle agent of the example rig leave no finding open: a request that declares the output `parked` gets an empty list.
func putNoParked(rig *agentRig, request AgentRequest) {
	if slices.Contains(request.Outputs, "parked") {
		rig.put(request.OutputFile, `{"parked":"[]"}`)
	}
}

// exampleSkill returns the one skill that an agent of the example requires.
func exampleSkill(t *testing.T, folder string) string {
	t.Helper()

	skills, err := dryRequiredSkills(filepath.Join(folder, "workflow.star"))
	require.NoError(t, err)
	require.Len(t, skills, 1)

	return skills[0]
}

func (r *agentRig) models() []string {
	var models []string

	for _, request := range r.h.agents.requests {
		models = append(models, request.Model)
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

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

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
	}, func(r AgentRequest) bool { return r.Resume != "" }, agent.OutcomeBudgetCap)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "budget")
}

func TestExample_AnApprovedFirstReviewMarksTheHeadReadyWithoutAFix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models())
	assert.Equal(t, "merged", result.Merge)
}

func TestExample_AnApproveWithAnOpenFindingIsAChangesVerdictAndSendsTheRunToAFix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(int) string { return "approve" }, func(review int) string {
		if review == 1 {
			return `[{"class":"minor","text":"rename x"}]`
		}

		return "[]"
	})

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus"}, rig.models(), "the follow-up of review 1 was fixed, and review 2 approved")
	assert.Equal(t, "merged", result.Merge)
}

// libraryLoop is a workflow that runs the review loop of the library with the extra arguments.
func libraryLoop(extra string) string {
	return "load(\"@obee//review_fix.star\", \"review_fix\")\n" +
		"def main(item):\n" +
		"    impl = struct(violations = \"\", session = \"\", rewrote = False)\n" +
		"    review_fix(item, impl, \"prompts/review.md\", \"prompts/fix.md\", facts = facts()" + extra + ")\n"
}

func TestReviewFix_TheFollowUpsArgumentReachesTheReviewAndTheDefaultAllowsThem(t *testing.T) {
	t.Parallel()

	for extra, want := range map[string][]string{
		"":                        {"opus"},
		`, follow_ups = "allow"`:  {"opus"},
		`, follow_ups = "forbid"`: {"opus", "sonnet", "opus"},
	} {
		rig, file := exampleRigWithFindings(t, func(int) string { return "approve" }, func(review int) string {
			if review == 1 {
				return `[{"class":"deferred","text":"later"}]`
			}

			return "[]"
		})
		require.NoError(t, os.WriteFile(file, []byte(libraryLoop(extra)), 0o600))

		_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

		require.NoError(t, err, extra)
		assert.Equal(t, want, rig.models(), extra)
	}
}

func TestReviewFix_TheStatusShowsTheRoundsUsedAgainstTheCapsOfTheLoop(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(int) string { return "approve" }, func(review int) string {
		if review == 1 {
			return `[{"class":"deferred","text":"later"}]`
		}

		return "[]"
	})
	require.NoError(t, os.WriteFile(file, []byte(libraryLoop(`, follow_ups = "forbid", rounds = 4`)), 0o600))

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	info := onlyRun(t, rig.h.client(t, testVersion))

	assert.Equal(t, Rounds{Used: 1, Cap: 3}, info.Fix, "a loop of 4 rounds has 3 fix rounds")
	assert.Equal(t, Rounds{Used: 2, Cap: 4}, info.Review)
}

func TestExample_FiveFixRoundsAreEachFollowedByAReviewAndTheSixthReviewIsTheLast(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "changes" })
	engine := rig.h.engine(t)
	done := runInBackground2(engine, file)

	waitForTheOwner(t, rig.h)
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "park all"))
	outcome := <-done
	require.NoError(t, outcome.err)

	// plan, implement, then review and fix for five rounds (rounds 4 and 5 on opus), the sixth review, and the settle agent.
	want := []string{"opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus", "sonnet", "opus", "opus", "opus", "opus", "opus", "sonnet"}
	assert.Equal(t, want, rig.models())
}

func TestExample_AGateThatCannotRunEndsTheRunBeforeAnyReviewOrFix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.gate.err = errFake

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorIs(t, err, errFake)
	assert.Equal(t, []string{"opus", "sonnet"}, rig.models(), "only the plan and the implement agents ran")
	assert.Equal(t, 1, rig.h.gate.calls, "no second round")
}

// packageGate runs the real gate package on a local project whose gate command is killed by a signal on the first run and exits 0 on the next.
type packageGate struct {
	head, tree string
	runs       *int
}

func (packageGate) Submit(context.Context, string, string, string) (GateJob, error) {
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

func (hostDiedGate) Submit(context.Context, string, string, string) (GateJob, error) {
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

func (zeroTestGate) Submit(context.Context, string, string, string) (GateJob, error) {
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

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "sonnet", "opus"}, rig.models(), "round 1 runs the fix and no review: the gate did not pass")
	assert.Contains(t, rig.h.agents.requests[2].Prompt, "hold 0 tests", "the reason reaches the fix")
	assert.Equal(t, 2, runs)
}

func TestExample_AGateKilledByASignalIsVoidAndRerunWithNoFixAgent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	runs := 0
	rig.h.gate.via = packageGate{head: rig.head, tree: exampleTree, runs: &runs}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models(), "a killed gate is void, not red: no fix agent, and the review waits for the rerun")
	assert.Equal(t, 2, runs, "the rerun is a new gate step")
}

func TestExample_AHostGateThatDiedIsVoidAndRerunWithNoFixAgent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	waits := 0
	rig.h.gate.via = hostDiedGate{head: rig.head, tree: exampleTree, waits: &waits}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models(), "a died job is void, not red: no fix agent, and the review waits for the rerun")
	assert.Equal(t, 2, waits, "the rerun is a new gate step")
}

func voidResult(rig *agentRig, report string) GateResult {
	return GateResult{Passed: false, Head: rig.head, Tree: exampleTree, Report: report, Steps: stepsUnknown}
}

func TestExample_AVoidGateIsRerunInTheRunAndAPassAfterItGivesAPassWithNoFixAgent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.gate.results = []GateResult{voidResult(rig, "the gate wrote no report"), {Passed: true, Head: rig.head, Tree: exampleTree}}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models(), "a void gate is not red: the implementer and the review run, and no fix agent")
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
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	for range 3 {
		eventually(t, func() bool { return client.Answer(testItem, "run it again") == nil })
	}

	outcome := <-done

	require.ErrorContains(t, outcome.err, "the gate gave no result")
	require.ErrorContains(t, outcome.err, "tests", "the refusal names the step with no result, also when the report is empty")
	assert.Equal(t, []string{"opus", "sonnet"}, rig.models(), "no fix agent starts")
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
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "run it again") == nil })

	require.NoError(t, (<-done).err)
	assert.Equal(t, 4, rig.h.gate.calls, "the answer, which the refusal-free question names, ran the gate again")
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models())
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
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	// The red gate repeats until the rounds cap, and then the run waits for a person for the whole claim lease. The test ends it after
	// the second gate, which is the claim of the test.
	eventually(t, func() bool {
		rig.h.gate.mu.Lock()
		defer rig.h.gate.mu.Unlock()

		return rig.h.gate.calls > 1
	})
	require.NoError(t, client.Cancel(testItem))
	rig.h.tick(t)

	err := (<-done).err

	assert.NotContains(t, fmt.Sprint(err), "the gate gave no result")
}

func TestExample_AReviewIsOnlyReadAgainstTheHeadTheGatePassed(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.gate.results = []GateResult{{Passed: true, Head: "0000000000000000000000000000000000000000", Tree: exampleTree}}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "0000000000000000000000000000000000000000")
	require.ErrorContains(t, err, rig.head)
	assert.Equal(t, []string{"opus", "sonnet"}, rig.models(), "the review agent never started")
}

func TestExample_AGateThatRecordedNoTreeStopsTheRunAtTheMergeCall(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.gate.results = []GateResult{{Passed: true, Head: rig.head}}
	rig.h.opts.Env["MERGE"] = "true"

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "`tree` is empty", "a merge needs the tree of the gate, so a gate that records none stops the run")
}

func TestExample_AReviewThatWritesNoVerdictFileIsAskedOnceMore(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(review int) string {
		if review == 1 {
			return ""
		}

		return "approve"
	})

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "opus"}, rig.models(), "the second review ran in the same round")
	assert.Equal(t, "merged", result.Merge)
}

func TestExample_AFailedGateSkipsTheReviewAndSendsItsReportToTheFix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.gate.results = []GateResult{{Passed: false, Head: rig.head, Report: "lint: unused variable x"}, {Passed: true, Head: rig.head, Tree: exampleTree}}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "sonnet", "opus"}, rig.models(), "round 1 runs the fix and no review; round 2 reviews")

	fix := rig.h.agents.requests[2].Prompt
	assert.Contains(t, fix, "lint: unused variable x", "the gate report is the findings of the fix")
}

func runInBackground2(engine *Engine, file string) <-chan runOutcome {
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	return done
}
