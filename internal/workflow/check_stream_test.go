package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

func TestCheck_AcceptsAStreamCallWithEachKeywordAndItsResultFields(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    runs = spawn([\"1\"])\n    s = stream(runs.runs[0], after = \"item-1-r1-phase/3\", timeout = \"5s\", name = \"p\")\n    x = [s.entries, s.next, s.ended]\n")

	assert.Empty(t, got)
}

func TestCheck_AStreamCallThatBreaksARuleIsRefusedWithTheRule(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ call, want string }{
		"no timeout":       {`stream(w)`, "stream needs the `timeout` keyword"},
		"zero timeout":     {`stream(w, timeout = "0s")`, "timeout is \"0s\", which must be above zero"},
		"after not text":   {`stream(w, after = 3, timeout = "5s")`, "after"},
		"after not cursor": {`stream(w, after = "later", timeout = "5s")`, "not a cursor that stream returned"},
		"no run":           {`stream(timeout = "5s")`, "stream needs the `run` keyword"},
		"unknown argument": {`stream(w, timeout = "5s", limit = 3)`, "limit"},
		"unknown field":    {"s = stream(w, timeout = \"5s\")\n    x = s.bogus", "bogus"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    w = spawn([\"1\"]).runs[0]\n    "+c.call+"\n")

			require.Len(t, got, 1)
			assert.Contains(t, got[0], c.want)
		})
	}
}

func TestParseCursor_ReadsTheTextThatStreamReturnsAndRefusesAnyOther(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]workflow.StreamCursor{"": {}, "run-1-phase/1": {Phase: "run-1-phase", Seq: 1}} {
		got, err := workflow.ParseCursor(text)

		require.NoError(t, err)
		assert.Equal(t, want, got)
		assert.Equal(t, text, got.String(), "the cursor makes the text again")
	}

	for _, text := range []string{"x", "/3", "x/", "x/0", "x/-1", "x/03", "x/1.5", "x/ 2", "a/b/12"} {
		_, err := workflow.ParseCursor(text)

		require.ErrorContains(t, err, "not a cursor that stream returned", text)
	}
}
