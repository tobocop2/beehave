package papers_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/examples/remediation/tools/papers"
)

const (
	meEmail = "me@x"
	meLogin = "me"
	okLine  = "author check: %s commits, %s ours and correctly addressed"

	wrongText    = "these commits are OURS but carry the wrong address"
	unlinkedText = "these commits have no linked account"
	bothText     = "--email and --login must both be given"
)

func TestAuthors_Verdicts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, text, email, login string
		code                     int
		out                      string
	}{
		{"our_commits_pass", "aaa me@x me\nbbb third@y them\n", meEmail, meLogin, 0, "2 commits, 1 ours"},
		{"third_party_keeps_address", "aaa third@y them\n", meEmail, meLogin, 0, "1 commits, 0 ours"},
		{"crlf_stays_in_login", "aaa other@y me\r\nbbb me@x me\r\n", meEmail, meLogin, 0, "2 commits, 1 ours"},
		{"cr_after_address_differs", "aaa me@x\r me\n", meEmail, meLogin, 3, wrongText},
		{"lone_cr_does_not_split", "aaa me@x me\rbbb other@y me\n", meEmail, meLogin, 0, "1 commits, 1 ours"},
		{"last_line_without_newline", "aaa me@x me", meEmail, meLogin, 0, "1 commits, 1 ours"},
		{"blank_lines_and_tabs", "aaa\tme@x\tme\n\n  \n", meEmail, meLogin, 0, "2 commits, 1 ours"},
		{"short_line_one_field", "aaa\n", meEmail, meLogin, 0, "1 commits, 0 ours"},
		{"short_line_two_fields", "aaa me@x\n", meEmail, meLogin, 0, "1 commits, 1 ours"},
		{"our_address_on_other_login", "aaa me@x them\n", meEmail, meLogin, 0, "1 commits, 1 ours"},
		{"unlinked_under_our_address", "aaa me@x ?\n", meEmail, meLogin, 0, "1 commits, 1 ours"},
		{"our_login_other_address", "aaa me@x me\nbbb other@y me\n", meEmail, meLogin, 3, wrongText},
		{"unlinked_other_address", "aaa other@x ?\nbbb me@x me\n", meEmail, meLogin, 3, unlinkedText},
		{"no_author", "aaa ? ?\n", meEmail, meLogin, 3, unlinkedText},
		{"missing_email", "aaa me@x me\n", "", meLogin, 2, bothText},
		{"missing_login", "aaa me@x me\n", meEmail, "", 2, bothText},
		{"missing_both", "aaa me@x me\n", "", "", 2, bothText},
		{"empty_input", "", meEmail, meLogin, 2, "no commit authors to check"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := papers.Authors(tc.text, tc.email, tc.login)

			assert.Equal(t, tc.code, got.Code)

			if tc.code == 0 {
				assert.Equal(t, []string{"author check: " + tc.out + " and correctly addressed"}, got.Out)
			} else {
				assert.Contains(t, strings.Join(got.Err, "\n"), tc.out)
			}
		})
	}
}

func TestAuthors_RefusalText(t *testing.T) {
	t.Parallel()

	wrong := papers.Authors("aaa me@x me\nbbb other@y me\n", meEmail, meLogin)
	assert.Equal(t, "REFUSE: these commits are OURS but carry the wrong address (expected me@x):", wrong.Err[0])
	assert.Equal(t, "  bbb other@y me", wrong.Err[1])
	assert.Len(t, wrong.Err, 4)

	many := papers.Authors("a x@y me\nb z@y me\n", meEmail, meLogin)
	assert.Equal(t, []string{"  a x@y me", "  b z@y me"}, many.Err[1:3])

	unlinked := papers.Authors("aaa other@x ?\n", meEmail, meLogin)
	assert.Equal(t, "REFUSE: these commits have no linked account and carry an address other than me@x:", unlinked.Err[0])
	assert.Contains(t, unlinked.Err[len(unlinked.Err)-1], "link the address to the account, or merge by hand")

	both := papers.Authors("aaa me@y me\nbbb other@x ?\n", meEmail, meLogin)
	assert.Contains(t, both.Err, "  aaa me@y me")
	assert.Contains(t, both.Err, "  bbb other@x ?")

	assert.Len(t, papers.Authors("aaa me@x me\n", "", meLogin).Err, 3)
	assert.Equal(t, []string{"REFUSE: no commit authors to check; refusing to call that clean"}, papers.Authors("", meEmail, meLogin).Err)
}
