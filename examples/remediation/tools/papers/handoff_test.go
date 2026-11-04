package papers_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/check"
	"github.com/tobocop2/beehave/examples/remediation/tools/papers"
)

const (
	handoffName = "prev.md"
	launch      = "launched: logs/sp-gate1.log, marker SP-GATE1-DONE\n"
)

func handoffText(regressions, tokens string) string {
	return "# Handoff: prev\n- Plan file: plans/x.md\n- Commit: abc\n- Done: a\n- Owed: b\n" +
		"- Do not redo: c\n- Regressions of this pull request so far: " + regressions + "\n" +
		"- Tokens at handoff: " + tokens + "\n"
}

// writeTurns writes one assistant message for each usage row: input, cache read, cache creation, output.
func writeTurns(t *testing.T, rows ...[]any) string {
	t.Helper()

	var text strings.Builder

	for _, row := range rows {
		entry := map[string]any{"type": "assistant", "isSidechain": true, "message": map[string]any{
			"id": row[0], "role": "assistant", "usage": map[string]any{
				"input_tokens": row[1], "cache_read_input_tokens": row[2], "cache_creation_input_tokens": row[3], "output_tokens": row[4],
			},
		}}

		encoded, err := json.Marshal(entry)
		require.NoError(t, err)
		text.Write(encoded)
		text.WriteString("\n")
	}

	return writeFile(t, "lane.jsonl", text.String())
}

func ptr(text string) *string { return &text }

func writeFile(t *testing.T, name, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func TestHandoff_Verdicts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, text, cap string
		refused         bool
		contains        []string
		lines           []string
	}{
		{"no_regression_line", "# Handoff: prev\n- Done: a\n- Tokens at handoff: 90000\n", "", true, []string{"has no line 'Regressions of this pull request so far: <n>'"}, nil},
		{"regression_not_a_number", handoffText("two", "90000"), "", true, nil, []string{"REFUSED: the regression count 'two' in prev.md is not a whole number."}},
		{"line_present_passes", handoffText("1", "90000"), "", false, nil, []string{"OK: regressions=1 tokens=90000"}},
		{"launched_over_cap", handoffText("0", "273000") + launch, "", true, []string{"holds 'launched:' and the lane is at 273000 tokens, over the cap of 150000", "Job not read because: <reason>"}, nil},
		{"launched_over_cap_with_reason", handoffText("0", "273000") + launch + "- Job not read because: the mutation run takes 3 hours\n", "", false, nil, nil},
		{"launched_under_cap", handoffText("0", "90000") + launch, "", true, []string{"holds 'launched:' and the lane is at 90000 tokens. A lane hands off", "never between a launch and its read"}, nil},
		{"launched_under_cap_with_reason", handoffText("0", "90000") + launch + "Job not read because: the orchestrator resumes this lane with the result\n", "", false, nil, nil},
		{"unknown_tokens_without_transcript", handoffText("0", "unknown (no report in the transcript)"), "", true, []string{"the spend of this lane is unknown", "--transcript <the lane's own transcript .jsonl>"}, nil},
		{"no_tokens_line_unknown_launch", "- Regressions of this pull request so far: 0\nlaunched: x\n", "", true, []string{"the lane is at an unknown number of tokens."}, nil},
		{"cap_decides_over", handoffText("0", "2000") + "launched: x\n", "1500", true, []string{"over the cap of 1500"}, nil},
		{"cap_above_count", handoffText("0", "2000") + "launched: x\n", "5000", true, []string{"the lane is at 2000 tokens. A lane hands off"}, nil},
		{"exactly_at_cap_not_over", handoffText("0", "1500") + "launched: x\n", "1500", true, []string{"the lane is at 1500 tokens. A lane hands off"}, nil},
		{"count_past_integer_range", handoffText("0", "99999999999999999999999") + "launched: x\n", "", true, []string{"over the cap of 150000"}, nil},
		{"crlf_regression_not_whole", strings.ReplaceAll(handoffText("1", "90000"), "\n", "\r\n"), "", true, []string{"REFUSED: the regression count '1\r' in prev.md is not a whole number."}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var given *string
			if tc.cap != "" {
				given = ptr(tc.cap)
			}

			got, err := papers.Handoff(tc.text, handoffName, papers.HandoffOptions{Cap: given})
			require.NoError(t, err)
			assert.Equal(t, tc.refused, got.Refused)

			if tc.lines != nil {
				assert.Equal(t, tc.lines, got.Lines)
			}

			for _, want := range tc.contains {
				assert.Contains(t, strings.Join(got.Lines, "\n"), want)
			}
		})
	}
}

func TestHandoff_Transcript(t *testing.T) {
	t.Parallel()

	lane := writeTurns(t, []any{"m1", 20, 170000, 10000, 300})
	unknown := handoffText("0", "unknown (no report in the transcript)")

	got, err := papers.Handoff(unknown, handoffName, papers.HandoffOptions{Transcript: lane})
	require.NoError(t, err)
	assert.Equal(t, []string{"OK: regressions=0 tokens=180020"}, got.Lines)

	got, err = papers.Handoff(unknown+"launched: logs/a.log, marker A-DONE\n", handoffName, papers.HandoffOptions{Transcript: lane})
	require.NoError(t, err)
	assert.True(t, got.Refused)
	assert.Contains(t, strings.Join(got.Lines, "\n"), "at 180020 tokens, over the cap")

	crlf := "- Regressions of this pull request so far: 1\n- Tokens at handoff: 5\r\n"
	got, err = papers.Handoff(crlf, handoffName, papers.HandoffOptions{Transcript: lane})
	require.NoError(t, err)
	assert.Equal(t, []string{"OK: regressions=1 tokens=180020"}, got.Lines)
}

func TestHandoff_FractionalCountIsNotComparedWithTheCap(t *testing.T) {
	t.Parallel()

	lane := writeTurns(t, []any{"m1", json.Number("20.0"), json.Number("170000.0"), json.Number("10000.0"), json.Number("300.0")})
	text := handoffText("0", "unknown") + "launched: logs/a.log, marker A-DONE\n"

	got, err := papers.Handoff(text, handoffName, papers.HandoffOptions{Transcript: lane})
	require.NoError(t, err)
	assert.Contains(t, strings.Join(got.Lines, "\n"), "at 180020.0 tokens. A lane hands off")
}

func TestHandoff_Refusals(t *testing.T) {
	t.Parallel()

	empty := writeFile(t, "empty.jsonl", "")

	for _, bad := range []string{"0", "abc", "-5", ""} {
		_, err := papers.Handoff(handoffText("0", "1"), handoffName, papers.HandoffOptions{Cap: ptr(bad)})
		require.ErrorContains(t, err, "is not a whole number of tokens above zero", bad)
	}

	_, err := papers.Handoff(handoffText("0", "unknown"), handoffName, papers.HandoffOptions{Transcript: empty})
	require.ErrorContains(t, err, "the lane's spend is unknown")

	_, err = papers.Handoff(handoffText("0", "unknown"), handoffName, papers.HandoffOptions{Transcript: filepath.Join(t.TempDir(), "none")})
	require.ErrorContains(t, err, "cannot read the lane transcript")
}

func TestHandoff_LinesRefusedBeforeATranscriptRefusalAreKept(t *testing.T) {
	t.Parallel()

	empty := writeFile(t, "empty.jsonl", "")

	got, err := papers.Handoff("- Tokens at handoff: unknown\n", "p.md", papers.HandoffOptions{Transcript: empty})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.True(t, got.Refused)
	assert.Equal(t, got.Lines, refusal.Lines)
	assert.Equal(t, fmt.Sprintf("REFUSED: %s has no line 'Regressions of this pull request so far: <n>'. Add it. A regression is a defect an", "p.md"), refusal.Lines[0])
}

func TestHandoff_LabelValues(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, text, want string }{
		{"plain", "Regressions of this pull request so far: 4\n", "4"},
		{"dash_and_blanks", "- Regressions of this pull request so far:   7  \n", "7"},
		{"absent_label", "Other: x\n", ""},
		{"star", "* Regressions of this pull request so far: 5\n", "5"},
		{"indented_line_skipped", "  Regressions of this pull request so far: 1\nRegressions of this pull request so far: 2\n", "2"},
		{"first_line_wins_even_when_empty", "Regressions of this pull request so far:\nRegressions of this pull request so far: 3\n", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := papers.Handoff(tc.text+"Tokens at handoff: 1\n", handoffName, papers.HandoffOptions{})
			require.NoError(t, err)

			if tc.want == "" {
				assert.Contains(t, strings.Join(got.Lines, "\n"), "has no line 'Regressions")
			} else {
				assert.Contains(t, strings.Join(got.Lines, "\n"), "regressions="+tc.want)
			}
		})
	}
}
