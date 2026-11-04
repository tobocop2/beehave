package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPROpen_OpensTheDraftOfTheItemAndReturnsItsNumberCreatedAndStack(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `opened = pr_open(title = "Fix 5", body = "text", issue = 5, base = "round-1", parent = "bb-4", name = "draft")
command(run = [str(opened.number), str(opened.created), opened.stack])
`)
	require.NoError(t, err)

	assert.Equal(t, []string{testProject + "|" + testItem + "|Fix 5|text|5|round-1|bb-4"}, f.opened)
	assert.Equal(t, []string{"7 True S_1"}, h.commands.ran())
}

func TestPROpen_TheOptionalKeywordsDefaultToNone(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `pr_open(title = "Fix", base = "main")
`)
	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|" + testItem + "|Fix||0|main|"}, f.opened)
}

func TestPROpen_EachCallIsItsOwnRecordedStep(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `a = pr_open(title = "Fix", base = "main", name = "draft")
b = pr_open(title = "Fix", base = "main", name = "again")
command(run = [str(a.created), str(b.created)])
`)
	require.NoError(t, err)
	assert.Len(t, f.opened, 2, "the fake forge answers the second call as a find, as the real forge does")
	assert.Equal(t, []string{"True False"}, h.commands.ran())
}

func TestPROpen_BadArgumentsFailAtTheCall(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"no title":         `pr_open(base = "main")`,
		"an empty title":   `pr_open(title = "", base = "main")`,
		"no base":          `pr_open(title = "t")`,
		"an empty base":    `pr_open(title = "t", base = "")`,
		"a negative issue": `pr_open(title = "t", base = "main", issue = -1)`,
		"a text issue":     `pr_open(title = "t", base = "main", issue = "5")`,
	} {
		h, f := prHarness(t)

		_, err := walkBody(t, h, call+"\n")
		require.Error(t, err, name)
		assert.Empty(t, f.opened, name)
	}
}

func TestPROpen_AFailedOpenFailsTheStep(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)
	f.writeErr = errFake

	_, err := walkBody(t, h, `pr_open(title = "t", base = "main")
`)
	require.ErrorIs(t, err, errFake)
}
