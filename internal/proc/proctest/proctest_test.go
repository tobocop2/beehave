package proctest_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/ghcontract"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

var _ proc.Runner = (*proctest.Scripted)(nil)

func TestScripted_AnswersByWordsAndTheLatestPlanWins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want model.CommandResult
	}{
		{"latest_plan_wins", []string{"issue", "list", "--json"}, model.CommandResult{Stdout: "second"}},
		{"failure_plan", []string{"close", "7"}, model.CommandResult{ExitCode: 1, Stderr: "down"}},
		{"argument_contains_text", []string{"api", "repos/o/n/pulls"}, model.CommandResult{Stdout: "found"}},
		{"contains_failure_plan", []string{"api", "compare/a...b"}, model.CommandResult{ExitCode: 1}},
		{"result_plan", []string{"build"}, model.CommandResult{ExitCode: 2, Stdout: "partial"}},
		{"no_plan_fits", []string{"show"}, model.CommandResult{ExitCode: 99, Stderr: "scripted: no answer planned for [show]"}},
		{"a_word_fits_anywhere_in_the_arguments", []string{"list", "issue"}, model.CommandResult{Stdout: "second"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			local := &proctest.Scripted{}
			local.Plan("second", "list")
			local.PlanFail("down", "close")
			local.PlanContaining("found", "repos/o/n")
			local.PlanContainingFail("compare/")
			local.PlanResult(model.CommandResult{ExitCode: 2, Stdout: "partial"}, "build")

			assert.Equal(t, tc.want, local.Run(t.Context(), tc.args))
			assert.Equal(t, [][]string{tc.args}, local.Calls)
		})
	}
}

func TestStrict_RefusesWhatTheContractRefuses(t *testing.T) {
	t.Parallel()

	run := proctest.Always(model.CommandResult{Stdout: "[]"}).Strict(ghcontract.Check)
	done := run.Run(t.Context(), []string{"pr", "list", "--json", "number,authorAssociation"})

	assert.Equal(t, 1, done.ExitCode)
	assert.Empty(t, done.Stdout)
	assert.True(t, strings.HasPrefix(done.Stderr, `Unknown JSON field: "authorAssociation"`), done.Stderr)
	assert.True(t, run.Called("pr", "list"), "a refused call is recorded")

	assert.Equal(t, 1, run.Run(t.Context(), []string{"pr", "list", "--nope"}).ExitCode)
	assert.Equal(t, 1, run.Run(t.Context(), []string{"gist", "list"}).ExitCode)
}

func TestChecked_RefusesWhatTheCheckRefusesOverAnyRunner(t *testing.T) {
	t.Parallel()

	inner := proctest.Always(model.CommandResult{Stdout: "[]"})
	run := proctest.Checked(inner, ghcontract.Check)

	refused := run.Run(t.Context(), []string{"pr", "list", "--json", "number,authorAssociation"})

	assert.Equal(t, 1, refused.ExitCode)
	assert.True(t, strings.HasPrefix(refused.Stderr, `Unknown JSON field: "authorAssociation"`), refused.Stderr)
	assert.Empty(t, inner.Calls, "a refused call never reaches the runner inside")

	accepted := run.Run(t.Context(), []string{"pr", "list", "--json", "number"})

	assert.Equal(t, model.CommandResult{Stdout: "[]"}, accepted)
	assert.True(t, inner.Called("pr", "list"))
}

func TestStrict_PassesACallTheContractAccepts(t *testing.T) {
	t.Parallel()

	run := proctest.Always(model.CommandResult{Stdout: "[]"})
	run.Plan(`[{"number":7}]`, "pr", "list")

	assert.Same(t, run, run.Strict(ghcontract.Check))

	done := run.Run(t.Context(), []string{"pr", "list", "--state", "open", "--json", "number"})

	assert.Equal(t, model.CommandResult{Stdout: `[{"number":7}]`}, done)
}

func TestScripted_RecordsWhatWasCalled(t *testing.T) {
	t.Parallel()

	run := proctest.Always(model.CommandResult{Stdout: "ok"})
	assert.Equal(t, "ok", run.Run(t.Context(), []string{"issue", "create", "--repo=o/n"}).Stdout)

	assert.True(t, run.Called("issue", "create"))
	assert.False(t, run.Called("create", "issue"))
	assert.True(t, run.CalledContaining("--repo="))
	assert.False(t, run.CalledContaining("--since="))
}
