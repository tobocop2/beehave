package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

func TestKeepIssues_ReadsOnlyTheKeepForm(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		text string
		want []model.IssueRef
	}{
		"two_own_issues":                       {"keep #13, #14", []model.IssueRef{"#13", "#14"}},
		"capitals_and_spaces":                  {"  KEEP   #13 ,#14  ", []model.IssueRef{"#13", "#14"}},
		"another_repository_in_lower_case":     {"Keep #13, Other-Org/Other-Repo#5", []model.IssueRef{"#13", "other-org/other-repo#5"}},
		"leading_zeros":                        {"keep #007", []model.IssueRef{"#7"}},
		"largest_number":                       {"keep #9223372036854775807", []model.IssueRef{"#9223372036854775807"}},
		"a_tab_after_the_word":                 {"keep\t#13", []model.IssueRef{"#13"}},
		"number_past_64_bits":                  {"keep #9223372036854775808", nil},
		"number_far_past_64_bits":              {"keep #99999999999999999999", nil},
		"other_repository_number_past_64_bits": {"keep other/repo#99999999999999999999", nil},
		"no_hash":                              {"keep 5", nil},
		"word_only":                            {"keep", nil},
		"no_space_after_the_word":              {"keep#13", nil},
		"trailing_comma":                       {"keep #13,", nil},
		"hash_only":                            {"keep #", nil},
		"letter_in_number":                     {"keep #1x", nil},
		"sign_in_number":                       {"keep #+1", nil},
		"repository_without_number":            {"keep other/repo", nil},
		"three_part_repository":                {"keep other/repo/more#5", nil},
		"no_owner":                             {"keep /repo#5", nil},
		"no_repository_name":                   {"keep owner/#5", nil},
		"repository_without_slash":             {"keep repo#5", nil},
		"space_in_owner":                       {"keep o wner/repo#5", nil},
		"words_after_an_issue":                 {"keep other-org/other-repo#5 and #6", nil},
		"word_not_first":                       {"no keep #13", nil},
		"empty":                                {"", nil},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, c.want, workflow.KeepIssues(c.text), "the text %q", c.text)
		})
	}
}
