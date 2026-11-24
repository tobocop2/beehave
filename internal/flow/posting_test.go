package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closingCalls holds, for each posting step of a pull request, a call that forbids a closing keyword and posts one in each text it posts.
var closingCalls = map[string]string{
	"pr_comment":    `pr_comment(text = "Fixes #12", key = "k", forbid_text = ["(?i)fixes #[0-9]+"])`,
	"pr_open title": `pr_open(title = "Fixes #12", base = "main", forbid_text = ["(?i)fixes #[0-9]+"])`,
	"pr_open body":  `pr_open(title = "fine", base = "main", body = "a\nFixes #12", forbid_text = ["(?i)fixes #[0-9]+"])`,
}

func TestPosting_AClosingKeywordThatTheWorkflowForbidsIsRefusedBeforeThePost(t *testing.T) {
	t.Parallel()

	for name, call := range closingCalls {
		h, f := prHarness(t)

		_, err := walkBody(t, h, call+"\n")

		require.ErrorContains(t, err, "forbid_text", name)
		assert.Empty(t, f.notes, name)
		assert.Empty(t, f.opened, name)
	}
}

func TestPosting_AClosingKeywordIsPostedWhenTheWorkflowDoesNotForbidIt(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `pr_comment(text = "Fixes #12", key = "a")
pr_comment(text = "Part of #12", key = "b", forbid_text = ["(?i)fixes #[0-9]+"])
pr_open(title = "Closes #12", base = "main")
`)

	require.NoError(t, err)
	assert.Len(t, f.notes, 2)
	assert.Len(t, f.opened, 1)
}
