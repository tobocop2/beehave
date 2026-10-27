package proctest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/proc/proctest"
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

func TestScripted_RecordsWhatWasCalled(t *testing.T) {
	t.Parallel()

	run := proctest.Always(model.CommandResult{Stdout: "ok"})
	assert.Equal(t, "ok", run.Run(t.Context(), []string{"issue", "create", "--repo=o/n"}).Stdout)

	assert.True(t, run.Called("issue", "create"))
	assert.False(t, run.Called("create", "issue"))
	assert.True(t, run.CalledContaining("--repo="))
	assert.False(t, run.CalledContaining("--since="))
}
