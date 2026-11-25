package flow

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// holdNote is a whole note of a lane, and holdText is the comment that hold_item writes for it.
const (
	holdNote = `{"question":"Should the cache drop old entries?","options":["Drop after a day","Keep them for ever"],"recommendation":"Drop after a day: the store grows without a bound."}`
	holdText = "Needs the owner: Should the cache drop old entries?\n\nOptions:\n1. Drop after a day\n2. Keep them for ever\n\nRecommendation: Drop after a day: the store grows without a bound."
)

// noteOutcome is what a run of hold_item gave: the error of the run, the comments and the statuses that the tracker writer got, and the step names that the run recorded.
type noteOutcome struct {
	err      error
	notes    []string
	statuses []string
	steps    []string
}

// runHoldItem runs hold_item on the item of the test with the note text raw.
func runHoldItem(t *testing.T, raw string) noteOutcome {
	t.Helper()

	h := newHarness(t)
	w := newFakeWriter()
	h.writer = w
	engine := h.engine(t)
	text := "load(\"@beehave//hold.star\", \"hold_item\")\n" + bodyFlow(fmt.Sprintf("hold_item(name = \"h\", id = \"1\", raw = %q)\n", raw))

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, text), modeOff())

	return noteOutcome{err: err, notes: w.notes, statuses: w.statuses, steps: stepNames(t, engine)}
}

func TestHold_AHoldWithNoRecommendationIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, raw, field string }{
		{"no recommendation", `{"question":"q","options":["a","b"]}`, "no recommendation"},
		{"a blank recommendation", `{"question":"q","options":["a","b"],"recommendation":"  "}`, "no recommendation"},
		{"a recommendation that is not text", `{"question":"q","options":["a","b"],"recommendation":7}`, "no recommendation"},
		{"one option", `{"question":"q","options":["a"],"recommendation":"a"}`, "fewer than two options"},
		{"no options", `{"question":"q","recommendation":"a"}`, "fewer than two options"},
		{"options that are not a list", `{"question":"q","options":"a b","recommendation":"a"}`, "fewer than two options"},
		{"an empty option", `{"question":"q","options":["a",""],"recommendation":"a"}`, "no option 2"},
		{"an option that is not text", `{"question":"q","options":["a",5],"recommendation":"a"}`, "no option 2"},
		{"a blank question", `{"question":" ","options":["a","b"],"recommendation":"a"}`, "no question"},
		{"a question that is not text", `{"question":5,"options":["a","b"],"recommendation":"a"}`, "no question"},
		{"text that is not JSON", `we need a ruling`, "not a JSON object"},
		{"text that is not JSON shows its start", `we need a ruling`, "we need a ruling"},
		{"a JSON list", `["a","b"]`, "not a JSON object"},
		{"long text that is not JSON shows 200 characters and ends", strings.Repeat("x", 300), strings.Repeat("x", 200) + "..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := runHoldItem(t, tc.raw)

			require.Error(t, got.err)
			assert.Contains(t, got.err.Error(), "REFUSE:")
			assert.Contains(t, got.err.Error(), tc.field, "the error names the field that the note lacks")
			assert.Empty(t, got.notes, "a refused note writes no comment")
			assert.Empty(t, got.statuses, "a refused note sets no status")
			assert.Empty(t, got.steps, "a refused note starts no step")
		})
	}
}

func TestHold_ANoteWithOptionsAndARecommendationIsCommentedAndHoldsTheItem(t *testing.T) {
	t.Parallel()

	got := runHoldItem(t, holdNote)

	require.NoError(t, got.err)
	assert.Equal(t, []string{testProject + "|1|" + testRun + ":hold-note|" + holdText}, got.notes, "one comment with the question, each option and the recommendation, under the key hold-note")
	assert.Equal(t, []string{testProject + "|1|hold"}, got.statuses, "one status, hold")
	assert.Equal(t, []string{"item_comment:h-note#1", "item_status:h-status#1"}, got.steps, "the comment comes first, then the status")
}
