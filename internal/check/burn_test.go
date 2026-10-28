package check_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/check"
)

const (
	tab        = "\t"
	laneReview = "Lane rev-628 review"
	laneFix    = "Lane fix-9 implement"
)

func callLine(clock string, cacheRead int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-09T%s:00.000Z","message":{"role":"assistant","usage":{"cache_read_input_tokens":%d,"output_tokens":10}}}`+"\n", clock, cacheRead)
}

func noteLine(clock, task, desc string, tokens int) string {
	content := fmt.Sprintf(`<task-notification>\n<task-id>%s</task-id>\n<summary>Agent \"%s\" finished</summary>\n<usage><subagent_tokens>%d</subagent_tokens><tool_uses>5</tool_uses></usage>`, task, desc, tokens)

	return fmt.Sprintf(`{"type":"user","timestamp":"2026-10-09T%s:00.000Z","message":{"role":"user","content":"%s"}}`+"\n", clock, content)
}

func ptr(text string) *string { return &text }

func TestLaneRows(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "t.jsonl", noteLine("10:00", "abc1", laneReview, 90000)+noteLine("10:30", "abc1", laneReview, 512000)+noteLine("10:40", "def2", laneFix, 80000))

	rows, over, err := check.LaneRows(path, 150000, nil)
	require.NoError(t, err)
	assert.False(t, over)
	assert.Equal(t, []string{"abc1" + tab + laneReview + tab + "512000" + tab + "OVER", "def2" + tab + laneFix + tab + "80000" + tab + "under"}, rows)

	for name, want := range map[string]bool{"abc1": true, "rev-628": true, "fix-9": false, "def2": false} {
		rows, over, err = check.LaneRows(path, 150000, ptr(name))
		require.NoError(t, err)
		assert.Len(t, rows, 1, name)
		assert.Equal(t, want, over, name)
	}

	_, over, err = check.LaneRows(path, 50000, ptr("def2"))
	require.NoError(t, err)
	assert.True(t, over)
}

func TestLaneRows_OrderIsLastReportNotName(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "t.jsonl", noteLine("10:00", "zz9", "Lane late-name", 1000)+noteLine("10:30", "abc1", "Lane early-name", 2000))

	rows, _, err := check.LaneRows(path, 150000, nil)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(rows[0], "zz9"))
	assert.True(t, strings.HasPrefix(rows[1], "abc1"))
}

func TestLaneRows_NameIsAWholeWordAndLatestWins(t *testing.T) {
	t.Parallel()

	whole := writeFile(t, "t.jsonl", noteLine("10:40", "def2", "Lane fix-90 implement", 80000)+noteLine("10:41", "ghi3", "Lane fix-9", 5))

	rows, _, err := check.LaneRows(whole, 150000, ptr("fix-9"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(rows[0], "ghi3"))

	_, _, err = check.LaneRows(writeFile(t, "t.jsonl", noteLine("10:40", "def2", "Lane fix-90 implement", 80000)), 150000, ptr("fix-9"))
	require.ErrorContains(t, err, "no lane 'fix-9' has reported")
	require.ErrorContains(t, err, "unknown, not under the cap")

	twice := writeFile(t, "t.jsonl", noteLine("10:00", "old1", laneFix, 700000)+noteLine("11:00", "new2", laneFix, 40000))

	rows, over, err := check.LaneRows(twice, 150000, ptr("fix-9"))
	require.NoError(t, err)
	assert.False(t, over)
	assert.True(t, strings.HasPrefix(rows[0], "new2"))
}

func TestLaneRows_NoDescriptionIsADash(t *testing.T) {
	t.Parallel()

	line := `{"timestamp":"2026-10-09T10:00:00.000Z","content":"<task-id>ab1</task-id> <subagent_tokens>7</subagent_tokens>"}` + "\n"

	rows, _, err := check.LaneRows(writeFile(t, "t.jsonl", line), 150000, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"ab1" + tab + "-" + tab + "7" + tab + "under"}, rows)
}

func TestHoursTable(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "t.jsonl", callLine("10:05", 300000)+callLine("10:06", 300000)+noteLine("10:30", "abc1", laneReview, 512000))

	lines, err := check.HoursTable(path, 12)
	require.NoError(t, err)
	assert.Equal(t, []string{"hour           calls  own_cache_read  own_output  lanes", "2026-10-09T10      2           0.6M          0k    0.5M"}, lines)
}

func TestHoursTable_ShowsOnlyTheLastHours(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "t.jsonl", callLine("09:05", 1)+callLine("10:05", 1))

	lines, err := check.HoursTable(path, 1)
	require.NoError(t, err)
	assert.Len(t, lines, 2)
	assert.Contains(t, lines[1], "T10")

	lines, err = check.HoursTable(path, 0)
	require.NoError(t, err)
	assert.Len(t, lines, 3)
}

func TestHoursTable_SkipsWhatIsNotAMainThreadCall(t *testing.T) {
	t.Parallel()

	side := `{"isSidechain":true,"timestamp":"2026-10-09T10:06:00.000Z","message":{"role":"assistant","usage":{"output_tokens":1}}}` + "\n"
	falsy := `{"isSidechain":0,"timestamp":"2026-10-09T10:07:00.000Z","message":{"role":"assistant","usage":{"output_tokens":1}}}` + "\n"
	text := `{"isSidechain":"yes","timestamp":"2026-10-09T10:08:00.000Z","message":{"role":"assistant","usage":{"output_tokens":1}}}` + "\n"
	broken := "{\"role\":\"assistant\",\"usage\": broken\n[1]\n"
	wrong := `{"timestamp":"2026-10-09T10:09:00.000Z","message":{"role":"assistant","usage":{"output_tokens":"x"}}}` + "\n"
	array := `{"isSidechain":[1],"timestamp":"2026-10-09T10:10:00.000Z","message":{"role":"assistant","usage":{}}}` + "\n"
	object := `{"isSidechain":{"a":1},"timestamp":"2026-10-09T10:11:00.000Z","message":{"role":"assistant","usage":{}}}` + "\n"
	empty := `{"isSidechain":[],"timestamp":"2026-10-09T10:12:00.000Z","message":{"role":"assistant","usage":{}}}` + "\n"
	null := `{"isSidechain":null,"timestamp":"2026-10-09T10:13:00.000Z","message":{"role":"assistant","usage":{}}}` + "\n"
	emptyObject := `{"isSidechain":{},"timestamp":"2026-10-09T10:14:00.000Z","message":{"role":"assistant","usage":{}}}` + "\n"
	path := writeFile(t, "t.jsonl", callLine("10:05", 5)+side+text+broken+wrong+array+object+falsy+empty+null+emptyObject)

	lines, err := check.HoursTable(path, 12)
	require.NoError(t, err)
	assert.Contains(t, lines[1], "2026-10-09T10      5")
}

func TestHoursTable_FractionalCountsAreSummed(t *testing.T) {
	t.Parallel()

	line := `{"timestamp":"2026-10-09T10:05:00.000Z","message":{"role":"assistant","usage":{"cache_read_input_tokens":1.5e6,"output_tokens":2500.5}}}` + "\n"

	lines, err := check.HoursTable(writeFile(t, "t.jsonl", line), 12)
	require.NoError(t, err)
	assert.Contains(t, lines[1], "1.5M")
	assert.Contains(t, lines[1], "3k")
}

func TestHoursTable_ALaneThatReportedInAnHourWithNoCall(t *testing.T) {
	t.Parallel()

	lines, err := check.HoursTable(writeFile(t, "t.jsonl", noteLine("10:30", "abc1", laneReview, 512000)), 12)
	require.NoError(t, err)
	assert.Equal(t, "2026-10-09T10      0           0.0M          0k    0.5M", lines[1])
}

func TestLaneRows_AnEmptyNameFindsNoLane(t *testing.T) {
	t.Parallel()

	_, _, err := check.LaneRows(writeFile(t, "t.jsonl", noteLine("10:00", "abc1", laneReview, 1)), 10, ptr(""))
	require.ErrorContains(t, err, "no lane '' has reported")
}

func TestHoursTable_UnreadableTranscript(t *testing.T) {
	t.Parallel()

	_, err := check.HoursTable(t.TempDir(), 12)
	require.ErrorContains(t, err, "Is a directory")

	_, err = check.HoursTable(filepath.Join(t.TempDir(), "none.jsonl"), 12)
	require.ErrorContains(t, err, "cannot read the transcript")
	require.ErrorContains(t, err, ": No such file or directory")

	_, _, err = check.LaneRows(filepath.Join(t.TempDir(), "none.jsonl"), 1, nil)
	require.ErrorContains(t, err, "cannot read the transcript")
}

func TestHoursTable_ALineIsOneJSONObjectWithKeysOfExactCase(t *testing.T) {
	t.Parallel()

	call := strings.TrimSuffix(callLine("10:05", 5), "\n")
	upperSide := `{"IsSidechain":true,"timestamp":"2026-10-09T10:06:00.000Z","message":{"role":"assistant","usage":{"output_tokens":1}}}` + "\n"
	upperStamp := `{"Timestamp":"2026-10-09T11:06:00.000Z","message":{"role":"assistant","usage":{"output_tokens":1}}}` + "\n"
	upperMessage := `{"timestamp":"2026-10-09T10:07:00.000Z","Message":{"role":"assistant","usage":{"output_tokens":1}}}` + "\n"
	twice := `{"timestamp":"2026-10-09T12:00:00.000Z","timestamp":"2026-10-09T10:08:00.000Z","message":{"role":"assistant","usage":{"output_tokens":1}}}` + "\n"
	trailing := call + ` {"x":1}` + "\n"
	stray := call + " }\n"
	notObject := `["usage","role":"assistant"]` + "\n"
	nullLine := "null \"usage\" \"role\":\"assistant\"\n"
	badMessage := `{"x":{"role":"assistant"},"usage":1,"message":"text"}` + "\n"
	badUsage := `{"message":{"role":"assistant","usage":"text"}}` + "\n"

	lines, err := check.HoursTable(writeFile(t, "t.jsonl", call+"\n"+upperSide+upperStamp+upperMessage+twice+trailing+stray+notObject+nullLine+badMessage+badUsage), 12)
	require.NoError(t, err)
	joined := strings.Join(lines, "\n")

	assert.Len(t, lines, 3, joined)
	assert.Contains(t, joined, "2026-10-09T10      4")
	assert.NotContains(t, joined, "T11")
}

func TestHoursTable_ALineLongerThanTheScannerBufferIsRead(t *testing.T) {
	t.Parallel()

	long := `{"note":"` + strings.Repeat("x", 300000) + `"}` + "\n"

	lines, err := check.HoursTable(writeFile(t, "t.jsonl", long+callLine("10:05", 5)), 12)
	require.NoError(t, err)
	assert.Contains(t, lines[1], "2026-10-09T10      1")
}

func TestHoursTable_AFinalLineWithNoLineEndIsRead(t *testing.T) {
	t.Parallel()

	lines, err := check.HoursTable(writeFile(t, "t.jsonl", strings.TrimSuffix(callLine("10:05", 5), "\n")), 12)
	require.NoError(t, err)
	assert.Contains(t, lines[1], "2026-10-09T10      1")
}

func TestLaneRows_OneReplacementMarkIsWrittenForEachRunOfBadBytes(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, desc, want string }{
		{"valid_text_stays", "Lane é", "Lane é"},
		{"two_stray_bytes", "ab\xff\xfecd", "ab��cd"},
		{"lone_continuation_byte", "ab\x80cd", "ab�cd"},
		{"overlong_lead_and_its_byte", "ab\xc0\xafcd", "ab��cd"},
		{"cut_2_byte_sequence", "ab\xc3cd", "ab�cd"},
		{"cut_3_byte_sequence_after_one_byte", "ab\xe2cd", "ab�cd"},
		{"cut_3_byte_sequence_after_two_bytes", "ab\xe2\x82cd", "ab�cd"},
		{"cut_4_byte_sequence_after_one_byte", "ab\xf0z", "ab�z"},
		{"cut_4_byte_sequence_after_two_bytes", "ab\xf0\x9fz", "ab�z"},
		{"cut_4_byte_sequence_after_three_bytes", "ab\xf0\x9f\x98z", "ab�z"},
		{"cut_4_byte_sequence_with_a_lead_above_f0", "ab\xf1\x80z", "ab�z"},
		{"cut_sequence_then_a_valid_character", "ab\xe2\x82é", "ab�é"},
		{"cut_sequence_at_the_end", "ab\xe2\x82", "ab�"},
		{"lead_of_a_3_byte_sequence_with_an_overlong_second_byte", "a\xe0\x80\x80b", "a���b"},
		{"surrogate_is_three_bad_units", "a\xed\xa0\x80b", "a���b"},
		{"above_the_last_code_point", "a\xf4\x90\x80\x80b", "a����b"},
		{"a_written_replacement_mark_stays_one", "a�b", "a�b"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rows, _, err := check.LaneRows(writeFile(t, "t.jsonl", noteLine("10:00", "abc1", test.desc, 7)), 10, nil)
			require.NoError(t, err)
			assert.Equal(t, []string{"abc1" + tab + test.want + tab + "7" + tab + "under"}, rows)
		})
	}
}

func TestLaneRows_AnEmptyNameMatchesAtAnyBoundaryWithNoNameCharacterOnEitherSide(t *testing.T) {
	t.Parallel()

	rows, _, err := check.LaneRows(writeFile(t, "t.jsonl", noteLine("10:00", "abc1", "(x)", 7)), 10, ptr(""))
	require.NoError(t, err)
	assert.Len(t, rows, 1)

	_, _, err = check.LaneRows(writeFile(t, "t.jsonl", noteLine("10:00", "abc1", "x", 7)), 10, ptr(""))
	require.ErrorContains(t, err, "no lane '' has reported")
}

func TestLaneCap(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		arg  *string
		env  map[string]string
		want int64
		fail bool
	}{
		{"default", nil, nil, 150000, false},
		{"above_the_32_bit_range", ptr("3000000000"), nil, 3000000000, false},
		{"argument", ptr("50000"), map[string]string{"LANE_CONTEXT_CAP": "60000"}, 50000, false},
		{"environment", nil, map[string]string{"LANE_CONTEXT_CAP": "60000"}, 60000, false},
		{"zero", ptr("0"), nil, 0, true},
		{"negative", ptr("-1"), nil, 0, true},
		{"word", ptr("x"), nil, 0, true},
		{"empty_argument", ptr(""), nil, 0, true},
		{"arabic_indic_digit", ptr("٣"), nil, 0, true},
		{"superscript_two", ptr("²"), nil, 0, true},
		{"too_large_is_the_largest_cap", ptr("99999999999999999999999"), nil, math.MaxInt64, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := check.LaneCap(tc.arg, tc.env)
			if tc.fail {
				require.ErrorContains(t, err, "is not a whole number of tokens above zero")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestTranscriptPath(t *testing.T) {
	t.Parallel()

	config := t.TempDir()
	project := filepath.Join(config, "projects", "p")
	require.NoError(t, os.MkdirAll(project, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(project, "s1.jsonl"), nil, 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(config, "projects", "q"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config, "projects", "q", "s2.jsonl"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(project, "s2.jsonl"), nil, 0o600))

	require.NoError(t, os.MkdirAll(filepath.Join(config, "projects", ".hid"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config, "projects", ".hid", "s1.jsonl"), nil, 0o600))

	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "projects", "h"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "projects", "h", "s3.jsonl"), nil, 0o600))

	got, err := check.TranscriptPath("given.jsonl", map[string]string{"TRANSCRIPT": "env.jsonl"})
	require.NoError(t, err)
	assert.Equal(t, "given.jsonl", got)

	got, err = check.TranscriptPath("", map[string]string{"TRANSCRIPT": "env.jsonl"})
	require.NoError(t, err)
	assert.Equal(t, "env.jsonl", got)

	got, err = check.TranscriptPath("", map[string]string{"CLAUDE_CODE_SESSION_ID": "s1", "CLAUDE_CONFIG_DIR": config})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(project, "s1.jsonl"), got)

	got, err = check.TranscriptPath("", map[string]string{"CLAUDE_CODE_SESSION_ID": "s3", "HOME": home})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".claude", "projects", "h", "s3.jsonl"), got)

	_, err = check.TranscriptPath("", map[string]string{"CLAUDE_CODE_SESSION_ID": "s1"})
	require.ErrorContains(t, err, "set TRANSCRIPT, HOME or CLAUDE_CONFIG_DIR")

	_, err = check.TranscriptPath("", map[string]string{"CLAUDE_CONFIG_DIR": config})
	require.ErrorContains(t, err, "no transcript. Pass its path, or set TRANSCRIPT. Looked for <no CLAUDE_CODE_SESSION_ID>.jsonl under "+config+"/projects and found 0.")

	_, err = check.TranscriptPath("", map[string]string{"CLAUDE_CODE_SESSION_ID": "s2", "CLAUDE_CONFIG_DIR": config})
	require.ErrorContains(t, err, "Looked for s2.jsonl under "+config+"/projects and found 2.")

	_, err = check.TranscriptPath("", map[string]string{"CLAUDE_CODE_SESSION_ID": "s1", "CLAUDE_CONFIG_DIR": t.TempDir()})
	require.ErrorContains(t, err, "found 0.", "a config folder with no projects holds none")

	_, err = check.TranscriptPath("", map[string]string{"CLAUDE_CODE_SESSION_ID": "p/s1", "CLAUDE_CONFIG_DIR": config})
	require.ErrorContains(t, err, "found 0.", "a path in the id is not an id")

	_, err = check.TranscriptPath("", map[string]string{"CLAUDE_CODE_SESSION_ID": "s*", "CLAUDE_CONFIG_DIR": config})
	require.ErrorContains(t, err, "found 0.", "a glob character in the id is literal")
}

func TestLaneTranscriptLine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		rows []string
		want string
	}{
		{"repeated_id_counts_once", []string{
			`{"message":{"id":"m1","role":"assistant","usage":{"input_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":40000,"output_tokens":100}}}`,
			`{"message":{"id":"m2","role":"assistant","usage":{"input_tokens":10,"cache_read_input_tokens":40000,"cache_creation_input_tokens":5000,"output_tokens":200}}}`,
			`{"message":{"id":"m2","role":"assistant","usage":{"input_tokens":10,"cache_read_input_tokens":40000,"cache_creation_input_tokens":5000,"output_tokens":200}}}`,
			`{"message":{"id":"m3","role":"assistant","usage":{"input_tokens":20,"cache_read_input_tokens":170000,"cache_creation_input_tokens":10000,"output_tokens":300}}}`,
		}, "calls=3\tcontext=180020\ttotal=265640"},
		{"no_id_counts_each_line", []string{
			`{"message":{"role":"assistant","usage":{"input_tokens":1,"cache_read_input_tokens":1,"cache_creation_input_tokens":1,"output_tokens":1}}}`,
			`{"message":{"role":"assistant","usage":{"input_tokens":1,"cache_read_input_tokens":1,"cache_creation_input_tokens":1,"output_tokens":1}}}`,
		}, "calls=2\tcontext=3\ttotal=8"},
		{"fraction_prints_as_written", []string{
			`{"message":{"id":"m1","role":"assistant","usage":{"input_tokens":10.5,"cache_read_input_tokens":4,"output_tokens":1}}}`,
		}, "calls=1\tcontext=14.5\ttotal=15.5"},
		{"whole_fraction_keeps_its_point", []string{
			`{"message":{"id":"m1","role":"assistant","usage":{"input_tokens":20.0,"output_tokens":1}}}`,
		}, "calls=1\tcontext=20.0\ttotal=21.0"},
		{"large_and_small_fractions", []string{
			`{"message":{"id":"m1","role":"assistant","usage":{"input_tokens":1e16,"output_tokens":0.00001}}}`,
		}, "calls=1\tcontext=1e+16\ttotal=1e+16"},
		{"small_fraction_uses_exponent", []string{
			`{"message":{"id":"m1","role":"assistant","usage":{"input_tokens":0.00001}}}`,
		}, "calls=1\tcontext=1e-05\ttotal=1e-05"},
		{"zero_fraction", []string{
			`{"message":{"id":"m1","role":"assistant","usage":{"input_tokens":0.0}}}`,
		}, "calls=1\tcontext=0.0\ttotal=0.0"},
		{"lone_cr_and_crlf_end_lines", []string{
			`{"message":{"id":"m1","role":"assistant","usage":{"input_tokens":1}}}` + "\r" + `{"message":{"id":"m2","role":"assistant","usage":{"input_tokens":2}}}` + "\r\n" + `{"message":{"id":"m3","role":"assistant","usage":{"input_tokens":3}}}`,
		}, "calls=3\tcontext=3\ttotal=6"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := check.LaneTranscriptLine(writeFile(t, "lane.jsonl", strings.Join(tc.rows, "\n")+"\n"))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLaneTranscriptLine_Refusals(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"empty":            "",
		"no_usage_marker":  "plain text\n",
		"not_assistant":    `{"message": {"role": "user", "usage": {"output_tokens": 1}}}` + "\nnot json \"usage\"\n",
		"usage_not_number": `{"message":{"role":"assistant","usage":{"input_tokens":"x"}}}` + "\n",
		"empty_usage":      `{"message":{"role":"assistant","usage":{}}}` + "\n",
	} {
		_, err := check.LaneTranscriptLine(writeFile(t, "lane.jsonl", text))
		require.ErrorContains(t, err, "the lane's spend is unknown", name)
	}

	_, err := check.LaneTranscriptLine(filepath.Join(t.TempDir(), "none"))
	require.ErrorContains(t, err, "cannot read the lane transcript")

	_, err = check.LaneTranscriptLine(t.TempDir())
	require.ErrorContains(t, err, "cannot read the lane transcript")
}
