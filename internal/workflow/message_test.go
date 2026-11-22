package workflow_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

func TestMessageName_AcceptsTheNamesThatTheRuleAllows(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"one letter":    "a",
		"digit first":   "9lives",
		"dots dashes":   "review.round-2_x",
		"longest":       strings.Repeat("a", 64),
		"not the guard": "beehave", // the reserved prefix has a dot
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, workflow.MessageName("topic", text))
		})
	}
}

func TestMessageName_RefusesAnEmptyBadOrTooLongName(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"empty":      "",
		"upper case": "Bad Topic",
		"space":      "a b",
		"dash first": "-a",
		"dot first":  ".a",
		"too long":   strings.Repeat("a", 65),
		"non ascii":  "café",
		"line break": "a\n",
		"slash":      "a/b",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := workflow.MessageName("topic", text)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "topic")
			assert.Contains(t, err.Error(), "Use 1 to 64 characters")
		})
	}
}

func TestMessageName_RefusesTheNamesThatBeehaveReserves(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"beehave.answer", "beehave.child", "beehave.", "beehave.inbox"} {
		err := workflow.MessageName("key", text)

		require.Error(t, err, text)
		assert.Contains(t, err.Error(), "key")
		assert.Contains(t, err.Error(), "reserved")
	}
}

func TestMessageValue_EncodesAValueAsJSON(t *testing.T) {
	t.Parallel()

	dict := starlark.NewDict(1)
	require.NoError(t, dict.SetKey(starlark.String("n"), starlark.MakeInt(3)))

	raw, err := workflow.MessageValue(dict)

	require.NoError(t, err)
	assert.JSONEq(t, `{"n":3}`, string(raw))
}

func TestMessageValue_RefusesAValueThatIsNotJSON(t *testing.T) {
	t.Parallel()

	fn := starlark.NewBuiltin("f", nil)

	_, err := workflow.MessageValue(fn)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "text, numbers, booleans, lists and dicts")
}

func TestMessageValue_RefusesAValueOverTheCap(t *testing.T) {
	t.Parallel()

	// The JSON text of a string holds two quotes, so this one is exactly at the cap.
	atCap := starlark.String(strings.Repeat("a", workflow.MaxMessageBytes-2))

	raw, err := workflow.MessageValue(atCap)
	require.NoError(t, err)
	assert.Len(t, raw, workflow.MaxMessageBytes)

	_, err = workflow.MessageValue(starlark.String(strings.Repeat("a", workflow.MaxMessageBytes-1)))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "65536")
}

func TestMessageValue_TheRefusalNamesAMessageAndNoPrompt(t *testing.T) {
	t.Parallel()

	_, err := workflow.MessageValue(starlark.NewBuiltin("f", nil))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "a message cannot hold this value")
	assert.NotContains(t, err.Error(), "prompt")
}

func TestMessageJSON_KeepsAJSONTextAndRefusesTextThatIsNotJSON(t *testing.T) {
	t.Parallel()

	raw, err := workflow.MessageJSON([]byte(`{"n": 1}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"n":1}`, string(raw))

	_, err = workflow.MessageJSON([]byte(`{"n": `))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not JSON")
}

func TestMessageJSON_RefusesATextOverTheCap(t *testing.T) {
	t.Parallel()

	_, err := workflow.MessageJSON([]byte(`"` + strings.Repeat("a", workflow.MaxMessageBytes) + `"`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the cap is 65536")
}

func TestTopics_ReadsTheTopicsOfAFirstOfCall(t *testing.T) {
	t.Parallel()

	got, err := workflow.Topics(starlark.NewList([]starlark.Value{starlark.String("stop"), starlark.String("go.now")}))
	require.NoError(t, err)
	assert.Equal(t, []string{"stop", "go.now"}, got)

	none, err := workflow.Topics(starlark.None)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestTopics_RefusesATopicThatIsBadOrReserved(t *testing.T) {
	t.Parallel()

	for name, topic := range map[string]string{"bad": "Bad Topic", "reserved": "beehave.child"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := workflow.Topics(starlark.NewList([]starlark.Value{starlark.String(topic)}))

			require.Error(t, err)
		})
	}

	_, err := workflow.Topics(starlark.MakeInt(3))
	require.Error(t, err, "topics is a list")
}

func TestFirstOfSources_NeedsRunsTopicsOrATimeout(t *testing.T) {
	t.Parallel()

	require.NoError(t, workflow.FirstOfSources(true, false, false))
	require.NoError(t, workflow.FirstOfSources(false, true, false))
	require.NoError(t, workflow.FirstOfSources(false, false, true))
	require.ErrorContains(t, workflow.FirstOfSources(false, false, false), "first_of needs something to wait for")
}

func TestSend_ACallWithNoToKeywordEndsWithTheHintOfThatKeyword(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    send(topic = \"t\", value = 1)\n")

	require.Len(t, got, 1)
	assert.True(t, strings.HasSuffix(got[0], "send needs the `to` keyword. Add to = ... to the call. "+workflow.SendTargetHint+"."), got[0])
}
