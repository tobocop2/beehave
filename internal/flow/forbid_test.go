package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forbidCalls holds, for each step that posts public text, a call whose text matches the pattern and a call whose text is clean.
// Each match sits in one argument that the step posts.
var forbidCalls = map[string]struct{ dirty, clean string }{
	"item_create title": {
		`item_create(title = "ok\nacme-secret here", key = "k", forbid_text = ["acme-[a-z]+"])`,
		`item_create(title = "fine", key = "k", forbid_text = ["acme-[a-z]+"])`,
	},
	"item_create body": {
		`item_create(title = "fine", body = "line\nacme-secret", key = "k", forbid_text = ["acme-[a-z]+"])`,
		`item_create(title = "fine", body = "line", key = "k", forbid_text = ["acme-[a-z]+"])`,
	},
	"item_comment": {
		`item_comment(id = "n-1", text = "see acme-secret", key = "k", forbid_text = ["acme-[a-z]+"])`,
		`item_comment(id = "n-1", text = "see nothing", key = "k", forbid_text = ["acme-[a-z]+"])`,
	},
	"pr_comment": {
		`pr_comment(text = "see acme-secret", key = "k", forbid_text = ["acme-[a-z]+"])`,
		`pr_comment(text = "see nothing", key = "k", forbid_text = ["acme-[a-z]+"])`,
	},
	"pr_open title": {
		`pr_open(title = "acme-secret", base = "main", forbid_text = ["acme-[a-z]+"])`,
		`pr_open(title = "fine", base = "main", forbid_text = ["acme-[a-z]+"])`,
	},
	"pr_open body": {
		`pr_open(title = "fine", base = "main", body = "a\nacme-secret", forbid_text = ["acme-[a-z]+"])`,
		`pr_open(title = "fine", base = "main", body = "a", forbid_text = ["acme-[a-z]+"])`,
	},
}

func TestForbidText_AMatchRefusesTheStepBeforeThePostAndNamesThePatternNotTheText(t *testing.T) {
	t.Parallel()

	for name, call := range forbidCalls {
		h, f := prHarness(t)
		w := newFakeWriter()
		h.writer = w

		_, err := walkBody(t, h, call.dirty+"\n")
		require.ErrorContains(t, err, `matches the forbidden pattern "acme-[a-z]+"`, name)
		assert.NotContains(t, err.Error(), "acme-secret", name)
		assert.Empty(t, w.created, name)
		assert.Empty(t, w.notes, name)
		assert.Empty(t, f.notes, name)
		assert.Empty(t, f.opened, name)
	}
}

func TestForbidText_CleanTextGoesOut(t *testing.T) {
	t.Parallel()

	for name, call := range forbidCalls {
		h, f := prHarness(t)
		w := newFakeWriter()
		h.writer = w

		_, err := walkBody(t, h, call.clean+"\n")
		require.NoError(t, err, name)
		assert.Len(t, w.created, boolInt(name == "item_create title" || name == "item_create body"), name)
		assert.Len(t, w.notes, boolInt(name == "item_comment"), name)
		assert.Len(t, f.notes, boolInt(name == "pr_comment"), name)
		assert.Len(t, f.opened, boolInt(name == "pr_open title" || name == "pr_open body"), name)
	}
}

func TestForbidText_TextWithNoPatternsGoesOut(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `pr_comment(text = "acme-secret", key = "k")`+"\n")
	require.NoError(t, err)
	assert.Len(t, f.notes, 1)
}

func TestForbidText_APatternThatIsNotRE2OrNotTextRefusesTheStep(t *testing.T) {
	t.Parallel()

	for name, patterns := range map[string]string{"not RE2": `["("]`, "not text": `[1]`, "computed not text": `[len("a")]`, "computed not RE2": `["(" + ""]`} {
		h, f := prHarness(t)

		_, err := walkBody(t, h, `pr_comment(text = "x", key = "k", forbid_text = `+patterns+`)`+"\n")
		require.Error(t, err, name)
		assert.Empty(t, f.notes, name)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}

	return 0
}
