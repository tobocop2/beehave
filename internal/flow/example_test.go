package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/syntax"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/review"
)

func mustRead(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // the tests name their own example files
	require.NoError(t, err)

	return data
}

const reviewMarker = "What every review here must do"

// exampleRig runs examples/remediation/workflow.hcl, copied with its prompts, over the fake collaborators.
// A review whose verdict is the empty string writes no output file.
func exampleRig(t *testing.T, verdicts func(review int) string) (*agentRig, string) {
	t.Helper()

	folder := t.TempDir()
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", "remediation"))))

	skill := exampleSkill(t, folder)

	rig := newAgentRig(t)
	rig.h.opts.Env = map[string]string{"REPO": "o/r", "BASE": "integration", "OWNER_LOGIN": "owner", "OWNER_EMAIL": "owner@example.com"}
	rig.h.gate.results = []GateResult{{Passed: true, Head: rig.head}}
	rig.h.forge.state = review.Standing{Number: testPull, Head: rig.head}

	reviews := 0

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		if strings.Contains(request.Prompt, reviewMarker) {
			reviews++

			_, rest, _ := strings.Cut(request.Prompt, "Write it to ")
			path, _, _ := strings.Cut(rest, " as JSON")
			if verdict := verdicts(reviews); verdict != "" {
				rig.put(path, `{"verdict":"`+verdict+`"}`)
			}
		}

		return agent.Result{Outcome: agent.OutcomeExited, Session: "sess-1", Transcript: "/t.jsonl", Text: "all done", Skills: []string{skill}}
	}

	return rig, filepath.Join(folder, "workflow.star")
}

// exampleSkill returns the one skill that an agent of the example requires.
func exampleSkill(t *testing.T, folder string) string {
	t.Helper()

	parsed, err := (&syntax.FileOptions{}).Parse("workflow.star", mustRead(t, filepath.Join(folder, "workflow.star")), 0)
	require.NoError(t, err)

	var skills []string

	syntax.Walk(parsed, func(n syntax.Node) bool {
		if pair, ok := n.(*syntax.BinaryExpr); ok && pair.Op == syntax.EQ {
			if name, isName := pair.X.(*syntax.Ident); isName && name.Name == "require_skills" {
				syntax.Walk(pair.Y, func(m syntax.Node) bool {
					if lit, isLit := m.(*syntax.Literal); isLit {
						skills = append(skills, fmt.Sprint(lit.Value))
					}

					return true
				})
			}
		}

		return true
	})
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

func TestExample_AnApprovedFirstReviewMarksTheHeadReadyWithoutAFix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models())
	assert.Equal(t, "merged", result.Merge)
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

func TestExample_AReviewIsOnlyReadAgainstTheHeadTheGatePassed(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.gate.results = []GateResult{{Passed: true, Head: "0000000000000000000000000000000000000000"}}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "0000000000000000000000000000000000000000")
	require.ErrorContains(t, err, rig.head)
	assert.Equal(t, []string{"opus", "sonnet"}, rig.models(), "the review agent never started")
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
	rig.h.gate.results = []GateResult{{Passed: false, Head: rig.head, Report: "lint: unused variable x"}, {Passed: true, Head: rig.head}}

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
