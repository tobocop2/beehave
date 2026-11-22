package flow

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the steps and the limits of the review loop: the library takes them as arguments, and the example reads them from vars.round.

// stepsOf builds vars.round.steps from a list of step, model and runner. An empty model leaves the key out, and an empty runner too.
func projectSteps(entries ...[3]string) map[string]any {
	steps := map[string]any{}

	for _, entry := range entries {
		step := map[string]any{}

		if entry[1] != "" {
			step["model"] = entry[1]
		}

		if entry[2] != "" {
			step["runner"] = entry[2]
		}

		steps[entry[0]] = step
	}

	return steps
}

// loopOfTheExample runs the example over the design rig with the steps of vars.round and returns the requests of its agents.
func loopOfTheExample(t *testing.T, change map[string]any) []AgentRequest {
	t.Helper()

	rig, file := exampleRigWithDesign(t, alwaysChanges, noFindings, sameMechanism, `{"decision":"replace","reason":"the framework already retries"}`)
	rig.h.writer = newFakeWriter()

	for key, value := range change {
		rig.setRound(key, value)
	}

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	return rig.h.agents.seen()
}

// modelsOf is the model of each request, in order.
func requestModels(requests []AgentRequest) []string {
	var models []string

	for _, request := range requests {
		models = append(models, request.Model)
	}

	return models
}

// loginsOf is the login of each request, in order.
func requestLogins(requests []AgentRequest) []string {
	var logins []string

	for _, request := range requests {
		logins = append(logins, request.Login)
	}

	return logins
}

func TestReviewFix_TheModelOfEachStepComesFromTheProjectFile(t *testing.T) {
	t.Parallel()

	requests := loopOfTheExample(t, map[string]any{"steps": projectSteps(
		[3]string{"plan", "m-plan", ""}, [3]string{"implement", "m-impl", ""}, [3]string{"review", "m-review", ""},
		[3]string{"fix", "m-fix", ""}, [3]string{"design", "m-design", ""},
	)})

	assert.Equal(t, []string{"m-plan", "m-impl", "m-impl", "m-review", "m-fix", "m-review", "m-fix", "m-review", "m-review", "m-design"}, requestModels(requests),
		"plan, implement, the sweep on the model of the implement step, three scoped reviews with two fixes, the full review, then the design step: each step runs on its own model of the project file")
}

func TestReviewFix_TheRunnerOfEachStepReachesTheAgentCall(t *testing.T) {
	t.Parallel()

	requests := loopOfTheExample(t, map[string]any{"steps": projectSteps(
		[3]string{"implement", "", "r-impl"}, [3]string{"review", "", "r-review"}, [3]string{"design", "", "r-design"},
	)})

	assert.Equal(t, []string{testLogin, "r-impl", "r-impl", "r-review", testLogin, "r-review", testLogin, "r-review", "r-review", "r-design"}, requestLogins(requests),
		"a step with a runner runs on it; the plan and the fix name none and keep the pool pick")
}

func TestReviewFix_AReviewOnTheRunnerOpencodeWithNoUsageReachesTheAgentCall(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithDesign(t, alwaysChanges, noFindings, sameMechanism, `{"decision":"replace","reason":"the framework already retries"}`)
	rig.h.writer = newFakeWriter()
	rig.h.picker = fakeLogins{err: errors.New("no runner has a usage reading")}

	steps := map[string]any{}
	for _, step := range []string{"plan", "implement", "review", "fix", "design"} {
		steps[step] = map[string]any{"runner": "opencode"}
	}

	rig.setRound("steps", steps)

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err, "the pool never picks a runner with no usage reading, and a named runner never reads the pool")

	for _, login := range requestLogins(rig.h.agents.seen()) {
		assert.Equal(t, "opencode", login)
	}
}

func TestReviewFix_TheLimitsComeFromTheProjectFile(t *testing.T) {
	t.Parallel()

	distinct := projectSteps([3]string{"plan", "m-plan", ""}, [3]string{"implement", "m-impl", ""}, [3]string{"review", "m-review", ""}, [3]string{"fix", "m-fix", ""}, [3]string{"design", "m-design", ""})

	// design_after 1: the design step runs after the first fix of the mechanism, not after the second.
	requests := loopOfTheExample(t, map[string]any{"steps": distinct, "design_after": json.Number("1")})
	assert.Equal(t, []string{"m-plan", "m-impl", "m-impl", "m-review", "m-fix", "m-review", "m-design"}, requestModels(requests), "design_after 1")

	// void_reruns 1 and void_asks 0: the second void gate has no rerun and no question left.
	_, _, err := lossyLoopWith(t, 2, 0, ", void_reruns = 1, void_asks = 0")
	require.ErrorContains(t, err, "after 1 reruns and 0 questions")

	// The default is two reruns: the same two voids end in a pass.
	value, _, err := lossyLoopWith(t, 2, 0, "")
	require.NoError(t, err)
	assert.True(t, value.Approved)
}

func TestReviewFix_TheLimitsOfTheProjectFileReachTheLoopOfTheExample(t *testing.T) {
	t.Parallel()

	distinct := projectSteps([3]string{"implement", "m-impl", ""}, [3]string{"review", "m-review", ""}, [3]string{"fix", "m-fix", ""}, [3]string{"fix_late", "m-late", ""})

	// escalate_after 0: the first fix round is already after it, so it starts fresh on fix_late.
	requests := loopOfTheExample(t, map[string]any{"steps": distinct, "escalate_after": json.Number("0")})

	var fixes []string

	for _, request := range requests {
		if strings.Contains(request.Prompt, "This is round 1.") {
			fixes = append(fixes, request.Model)
		}
	}

	assert.Equal(t, []string{"m-late", "m-late"}, fixes, "escalate_after 0 of the project file reaches the loop: the two fixes of candidate 1 start on fix_late")

	// void_reruns 0 and void_asks 0: the first void gate ends the item at once, with no question to the owner.
	// A loop that asks the owner parks the run, and the wait fails at that event, not at a bound.
	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.setRound("void_reruns", json.Number("0"))
	rig.setRound("void_asks", json.Number("0"))
	rig.h.gate.results = []GateResult{voidResult(rig, "")}

	client := rig.h.client(t, testVersion)
	done := pipelineInBackground(rig.h.engine(t), file)
	outcome := waitOn(t, waiting[runOutcome]{what: "the run to end with the void gate and no question to the owner", ch: done, run: runOfClient(t, client)})

	require.ErrorContains(t, outcome.err, "after 0 reruns and 0 questions")
	assert.Equal(t, 1, rig.h.gate.calls, "no rerun")
}

func TestReviewFix_ARoundAfterTheEscalationRunsFreshOnFixWhenTheProjectNamesNoFixLate(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, alwaysChanges)
	rig.setRound("steps", projectSteps([3]string{"implement", "m-impl", ""}, [3]string{"review", "m-review", ""}, [3]string{"fix", "m-fix", ""}))

	loop := strings.Replace(libraryLoop(rig.head, ", rounds = 4, escalate_after = 1"), `session = ""`, `session = "implement-session"`, 1)
	require.NoError(t, os.WriteFile(file, []byte(loop), 0o600))

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file, modeOff())
	require.NoError(t, err)

	seen := map[string]string{}

	for _, request := range rig.h.agents.seen() {
		for _, round := range []string{"1", "2", "3"} {
			if strings.Contains(request.Prompt, "This is round "+round+".") {
				seen[round] = request.Model + "/" + request.Resume
			}
		}
	}

	assert.Equal(t, map[string]string{"1": "m-fix/implement-session", "2": "m-fix/", "3": "m-fix/"}, seen, "the rounds after escalate_after start fresh on fix, as no fix_late entry exists")
}

func TestRunners_AProjectWithNoStepsRunsEveryStepOnItsDefaultModelAndThePool(t *testing.T) {
	t.Parallel()

	shown, err := settingsOf(t, roundVars(roundSettings()))
	require.NoError(t, err)

	for step, model := range map[string]string{
		"plan": "opus", "implement": "sonnet", "review": "opus", "fix": "sonnet", "fix_late": "opus", "design": "opus",
		"take_review": "opus", "sync_review": "opus", "sync_resolve": "opus", "queue_audit": "sonnet", "settle": "sonnet",
	} {
		assert.Contains(t, shown, `"`+step+`": {"model": "`+model+`", "runner": ""}`, step)
	}

	// The run: the plan, the implement step, the loop and the settle step read the defaults. take_review, sync_review and sync_resolve do the same in their tests.
	rig, file, _ := stopLoopRig(t, 15, map[string]any{"state": "MERGED"}, false)
	rig.setRound("fix_rounds", oneFixRound)
	rig.setRound("steps", nil)
	done := pipelineInBackground(rig.h.engine(t), file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "park all"}))
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, append([]string{"opus", "sonnet"}, capModels(2)...), rig.models(), "plan opus, implement sonnet, then the loop on review opus and fix sonnet")
}

func TestReviewFix_TheEscalationRoundComesFromTheProjectFile(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, alwaysChanges)
	rig.setRound("steps", projectSteps([3]string{"implement", "m-impl", ""}, [3]string{"review", "m-review", ""}, [3]string{"fix", "m-fix", ""}, [3]string{"fix_late", "m-late", ""}))

	loop := strings.Replace(libraryLoop(rig.head, ", rounds = 4, escalate_after = 1"), `session = ""`, `session = "implement-session"`, 1)
	require.NoError(t, os.WriteFile(file, []byte(loop), 0o600))

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file, modeOff())
	require.NoError(t, err)

	seen := map[string]string{}

	for _, request := range rig.h.agents.seen() {
		for _, round := range []string{"1", "2", "3"} {
			if strings.Contains(request.Prompt, "This is round "+round+".") {
				seen[round] = request.Model + "/" + request.Resume
			}
		}
	}

	assert.Equal(t, map[string]string{"1": "m-fix/implement-session", "2": "m-late/", "3": "m-late/"}, seen, "round 1 runs on fix and resumes the implementer, and the rounds after escalate_after start fresh on fix_late")
}

func TestReviewFix_AFixOnAnotherRunnerThanTheImplementerStartsAFreshSession(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		steps  map[string]any
		resume string
	}{
		"the same runner resumes":                {projectSteps([3]string{"implement", "m-impl", "oc"}, [3]string{"fix", "m-fix", "oc"}), "implement-session"},
		"the pool for both resumes":              {projectSteps([3]string{"implement", "m-impl", ""}, [3]string{"fix", "m-fix", ""}), "implement-session"},
		"another runner starts fresh":            {projectSteps([3]string{"implement", "m-impl", "oc"}, [3]string{"fix", "m-fix", "other"}), ""},
		"the pool and a runner start fresh":      {projectSteps([3]string{"implement", "m-impl", ""}, [3]string{"fix", "m-fix", "oc"}), ""},
		"a runner and the pool start fresh":      {projectSteps([3]string{"implement", "m-impl", "oc"}, [3]string{"fix", "m-fix", ""}), ""},
		"the model alone never decides a resume": {projectSteps([3]string{"implement", "m-impl", ""}, [3]string{"fix", "m-other", ""}), "implement-session"},
	} {
		rig, file := exampleRig(t, alwaysChanges)
		test.steps["review"] = map[string]any{"model": "m-review"}
		rig.setRound("steps", test.steps)

		loop := strings.Replace(libraryLoop(rig.head, ", rounds = 2"), `session = ""`, `session = "implement-session"`, 1)
		require.NoError(t, os.WriteFile(file, []byte(loop), 0o600), name)

		_, err := rig.h.engine(t).RunFile(testItem, testProject, file, modeOff())
		require.NoError(t, err, name)

		var fixes int

		for _, request := range rig.h.agents.seen() {
			if strings.Contains(request.Prompt, "This is round 1.") {
				fixes++

				assert.Equal(t, test.resume, request.Resume, name)
			}
		}

		assert.Positive(t, fixes, name)
	}
}

func TestReviewFix_ALibraryCallWithNoModelForAStepIsRefused(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		steps string
		want  string
	}{
		"no review":          {`{"implement": {"model": "m"}, "fix": {"model": "m"}}`, "no entry for the review step"},
		"no fix":             {`{"implement": {"model": "m"}, "review": {"model": "m"}}`, "no entry for the fix step"},
		"no implement":       {`{"review": {"model": "m"}, "fix": {"model": "m"}}`, "no entry for the implement step"},
		"a blank model":      {`{"implement": {"model": "m"}, "review": {"model": " "}, "fix": {"model": "m"}}`, "the review step of review_fix has no model"},
		"an entry of text":   {`{"implement": {"model": "m"}, "review": "m", "fix": {"model": "m"}}`, "the review step of review_fix has no model"},
		"a number as runner": {`{"implement": {"model": "m", "runner": 3}, "review": {"model": "m"}, "fix": {"model": "m"}}`, "the runner of the implement step"},
		"an unknown step":    {`{"implement": {"model": "m"}, "review": {"model": "m"}, "fix": {"model": "m"}, "plan": {"model": "m"}}`, "has no step plan"},
		"steps of a number":  {`3`, "got steps 3"},
	} {
		rig, file := exampleRig(t, alwaysChanges)
		loop := strings.Replace(libraryLoop(rig.head, ""), `steps = facts().vars["round"]["steps"]`, "steps = "+test.steps, 1)
		require.NoError(t, os.WriteFile(file, []byte(loop), 0o600), name)

		_, err := rig.h.engine(t).RunFile(testItem, testProject, file, modeOff())

		require.ErrorContains(t, err, "REFUSE", name)
		require.ErrorContains(t, err, test.want, name)
		assert.Empty(t, rig.h.agents.seen(), name+": no agent starts")
	}

	// The design step is needed only with a design prompt.
	rig, file := exampleRig(t, alwaysChanges)
	loop := strings.Replace(libraryLoop(rig.head, `, design_prompt = "prompts/design.md"`), `steps = facts().vars["round"]["steps"]`, `steps = {"implement": {"model": "m"}, "review": {"model": "m"}, "fix": {"model": "m"}}`, 1)
	require.NoError(t, os.WriteFile(file, []byte(loop), 0o600))

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file, modeOff())
	require.ErrorContains(t, err, "no entry for the design step")
}

func TestRunners_AReviewerOnTheModelOfTheImplementerItReviewsIsRefused(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		steps map[string]any
		want  string
	}{
		"the review model is the default model of the implement step": {projectSteps([3]string{"review", "sonnet", ""}), "vars.round.steps.review.model is \"sonnet\", the model of the implement step"},
		"the implement model moves to the default review model":       {projectSteps([3]string{"implement", "opus", ""}), "vars.round.steps.review.model is \"opus\""},
		"both steps on a third model":                                 {projectSteps([3]string{"implement", "m3", ""}, [3]string{"review", "m3", ""}), "vars.round.steps.review.model is \"m3\""},
	} {
		assert.Contains(t, refusalOf(t, roundVars(with(roundSettings(), map[string]any{"steps": test.steps}))), test.want, name)
	}

	// One model for every step has no other model to take, so it is accepted.
	solo := map[string]any{}
	for _, step := range []string{"plan", "implement", "review", "fix", "fix_late", "design", "take_review", "sync_review", "sync_resolve", "queue_audit", "settle"} {
		solo[step] = map[string]any{"model": "solo"}
	}

	_, err := settingsOf(t, roundVars(with(roundSettings(), map[string]any{"steps": solo})))
	require.NoError(t, err)
}

func TestRunners_TheStepsOfTheProjectFileAreCheckedWithTheKeyAndTheFix(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		steps any
		want  string
	}{
		"an unknown step":    {map[string]any{"revue": map[string]any{"model": "m"}}, "vars.round.steps.revue is not a step. The steps are: "},
		"an unknown field":   {map[string]any{"review": map[string]any{"mdl": "m"}}, "vars.round.steps.review.mdl is not a step field. The fields are: model, runner."},
		"a number as model":  {map[string]any{"review": map[string]any{"model": json.Number("3")}}, "vars.round.steps.review.model is"},
		"a blank model":      {map[string]any{"review": map[string]any{"model": " "}}, "vars.round.steps.review.model is \" \". Write the name of a model"},
		"a number as runner": {map[string]any{"review": map[string]any{"runner": json.Number("3")}}, "vars.round.steps.review.runner is"},
		"steps of a number":  {json.Number("3"), "for example {\"review\": {\"model\": \"your-model\"}}."},
		"an entry of text":   {map[string]any{"review": "m1"}, "vars.round.steps.review is \"m1\", of type string"},
	} {
		assert.Contains(t, refusalOf(t, roundVars(with(roundSettings(), map[string]any{"steps": test.steps}))), test.want, name)
	}
}

func TestRunners_TheStepsGiveEveryStepItsDefaultModelAndThePool(t *testing.T) {
	t.Parallel()

	shown, err := settingsOf(t, roundVars(with(roundSettings(), map[string]any{"steps": projectSteps([3]string{"review", "m1", "oc"})})))
	require.NoError(t, err)

	assert.Contains(t, shown, `"review": {"model": "m1", "runner": "oc"}`, "the step the project names")
	assert.Contains(t, shown, `"implement": {"model": "sonnet", "runner": ""}`, "a step the project leaves out keeps its default model and the pool")
	assert.Contains(t, shown, `"queue_audit": {"model": "sonnet", "runner": ""}`)
}

func TestRunners_TheLimitsOfTheLoopAreWholeNumbersInTheirRange(t *testing.T) {
	t.Parallel()

	for key, least := range map[string]int{"design_after": 1, "void_reruns": 0, "void_asks": 0, "escalate_after": 0} {
		for _, value := range []int{least, 100} {
			shown, err := settingsOf(t, roundVars(with(roundSettings(), map[string]any{key: json.Number(strconv.Itoa(value))})))

			require.NoError(t, err, key)
			assert.Contains(t, shown, `"`+key+`": `+strconv.Itoa(value), key)
		}

		for _, value := range []int{least - 1, 101} {
			assert.Contains(t, refusalOf(t, roundVars(with(roundSettings(), map[string]any{key: json.Number(strconv.Itoa(value))}))), "vars.round."+key+" is "+strconv.Itoa(value), key)
		}

		for _, wrong := range []any{"2", true} {
			got := refusalOf(t, roundVars(with(roundSettings(), map[string]any{key: wrong})))

			assert.Contains(t, got, "vars.round."+key+" is", key)
			assert.Contains(t, got, " to 100.", key+": the text ends with the fix, and no type name follows it")
		}
	}
}
