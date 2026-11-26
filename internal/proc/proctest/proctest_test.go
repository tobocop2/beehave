package proctest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

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

func TestScripted_RefusesAJSONFieldThatGHDoesNotAccept(t *testing.T) {
	t.Parallel()

	refused := func(field string) model.CommandResult {
		return model.CommandResult{ExitCode: 1, Stderr: `Unknown JSON field: "` + field + `"`}
	}
	planned := model.CommandResult{Stdout: "ok"}

	tests := []struct {
		name string
		args []string
		want model.CommandResult
	}{
		{"pr_list_author_association", []string{"pr", "list", "--repo", "o/n", "--json", "number,authorAssociation"}, refused("authorAssociation")},
		{"issue_list_author_association", []string{"issue", "list", "--json", "number,title,authorAssociation"}, refused("authorAssociation")},
		{"pr_view_issue_only_field", []string{"pr", "view", "7", "--json", "number,stateReason"}, refused("stateReason")},
		{"issue_view_pull_only_field", []string{"issue", "view", "7", "--json", "headRefName"}, refused("headRefName")},
		{"pr_list_known_fields", []string{"pr", "list", "--json", "number,headRefName,baseRefName,labels,reviewDecision,author,isDraft,title"}, planned},
		{"issue_list_known_fields", []string{"issue", "list", "--json", "number,title,labels,assignees"}, planned},
		{"pr_checks_has_other_fields", []string{"pr", "checks", "7", "--json", "name,bucket"}, planned},
		{"no_field_list", []string{"pr", "list", "--json"}, planned},
		{"not_gh", []string{"bd", "list", "--json", "authorAssociation"}, planned},
		{"no_arguments", []string{}, planned},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			run := proctest.Always(planned)
			assert.Equal(t, tc.want, run.Run(t.Context(), tc.args))
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
