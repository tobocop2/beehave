package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
)

const (
	blast  = "blast-radius-review"
	oracle = "diff-against-oracle"
)

func toJSON(t *testing.T, value any) string {
	t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(t, err)

	return string(raw)
}

func toolUse(tool, key, value string) map[string]any {
	return map[string]any{"type": "tool_use", "id": "toolu_1", "name": tool, "input": map[string]any{key: value}}
}

// skillLine is an assistant line holding one Skill call.
func skillLine(t *testing.T, skill string) string {
	t.Helper()

	return toJSON(t, map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{toolUse("Skill", "skill", skill)}}})
}

// blobLine is the second copy of a call that Claude Code writes in a request blob.
func blobLine(t *testing.T, skill, agentID string) string {
	t.Helper()

	return toJSON(t, map[string]any{"type": "api-request-blob", "agentId": agentID, "message": map[string]any{"content": []any{toolUse("Skill", "skill", skill)}}})
}

func agentSkillLine(t *testing.T, skill, agentID string) string {
	t.Helper()

	return toJSON(t, map[string]any{"type": "assistant", "isSidechain": true, "agentId": agentID, "message": map[string]any{"content": []any{toolUse("Skill", "skill", skill)}}})
}

func bashLine(t *testing.T, command string) string {
	t.Helper()

	return toJSON(t, map[string]any{"message": map[string]any{"content": []any{toolUse("Bash", "command", command)}}})
}

func TestBashCalls_ReadsOnlyBashCommandsAndNumbersTheLines(t *testing.T) {
	t.Parallel()

	other := toJSON(t, map[string]any{"message": map[string]any{"content": []any{toolUse("Read", "command", "git add -A")}}})
	noString := toJSON(t, map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": nil}}}}})
	noInput := toJSON(t, map[string]any{"name": "Bash"})
	text := toJSON(t, map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "git add -A"}}}})
	array := toJSON(t, []any{map[string]any{"name": "Bash", "input": map[string]any{"command": "git status"}}})
	path := writeFile(t, lines("not json", "", `{"type":"user","message":{"content":"git add -A"}}`, other, noString, noInput, text, "[1,2]", "{broken", bashLine(t, "ls"), array))

	got, err := agent.BashCalls(path)

	require.NoError(t, err)
	assert.Equal(t, []agent.Call{{Line: 10, Text: "ls"}, {Line: 11, Text: "git status"}}, got)
}

func TestBashCalls_CountsLoneCarriageReturnsAndCRLFAsLineEnds(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"lf":                    bashLine(t, "a") + "\n" + bashLine(t, "b") + "\n",
		"crlf":                  bashLine(t, "a") + "\r\n" + bashLine(t, "b") + "\r\n",
		"lone_cr":               bashLine(t, "a") + "\r" + bashLine(t, "b") + "\r",
		"no_final_newline":      bashLine(t, "a") + "\n" + bashLine(t, "b"),
		"cr_then_lf_in_a_chunk": bashLine(t, "a") + "\r\n" + bashLine(t, "b"),
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := agent.BashCalls(writeFile(t, content))

			require.NoError(t, err)
			assert.Equal(t, []agent.Call{{Line: 1, Text: "a"}, {Line: 2, Text: "b"}}, got)
		})
	}
}

func TestBashCalls_ABlankLineBetweenTwoCRsStillCountsAsALine(t *testing.T) {
	t.Parallel()

	got, err := agent.BashCalls(writeFile(t, "\r\r"+bashLine(t, "x")))

	require.NoError(t, err)
	assert.Equal(t, []agent.Call{{Line: 3, Text: "x"}}, got)
}

func TestBashCalls_TwoCallsOnOneLineKeepTheirOrder(t *testing.T) {
	t.Parallel()

	line := toJSON(t, map[string]any{"message": map[string]any{"content": []any{toolUse("Bash", "command", "first"), toolUse("Bash", "command", "second")}}})

	got, err := agent.BashCalls(writeFile(t, line+"\n"))

	require.NoError(t, err)
	assert.Equal(t, []agent.Call{{Line: 1, Text: "first"}, {Line: 1, Text: "second"}}, got)
}

func TestReadCalls_RefuseAnUnreadableFile(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "none.jsonl")

	_, bashErr := agent.BashCalls(missing)
	_, skillErr := agent.SkillCalls(missing, "")

	for _, err := range []error{bashErr, skillErr} {
		require.ErrorIs(t, err, agent.ErrUnreadable)
		assert.Contains(t, err.Error(), missing+": no such file or directory")
	}
}

func TestReadCalls_RefuseALineLongerThanTheReadLimit(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "long.jsonl")
	file, err := os.Create(path) //nolint:gosec // a path inside the test folder
	require.NoError(t, err)
	require.NoError(t, file.Truncate(300<<20)) // a sparse file of NUL bytes with no line end
	require.NoError(t, file.Close())

	_, err = agent.BashCalls(path)

	require.ErrorIs(t, err, agent.ErrUnreadable)
	assert.Contains(t, err.Error(), "token too long")
}

func TestSkillCalls_CountOnlyToolUseBlocksOfAssistantLines(t *testing.T) {
	t.Parallel()

	shaped := toolUse("Skill", "skill", blast)
	result := toJSON(t, map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "content": []any{shaped}}}}})
	extra := toJSON(t, map[string]any{"type": "user", "toolUseResult": shaped})
	text := toJSON(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "I used " + blast}}}})
	noContent := toJSON(t, map[string]any{"type": "assistant", "message": map[string]any{"content": "plain"}})
	noMessage := toJSON(t, map[string]any{"type": "assistant"})
	notBlock := toJSON(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []any{"text", 5}}})
	wrongTool := toJSON(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []any{toolUse("Read", "skill", blast)}}})
	notToolUse := toJSON(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "name": "Skill", "input": map[string]any{"skill": blast}}}}})
	notString := toJSON(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "Skill", "input": map[string]any{"skill": 5}}}}})
	path := writeFile(t, lines(result, extra, text, noContent, noMessage, notBlock, wrongTool, notToolUse, notString, "[1]", skillLine(t, oracle)))

	got, err := agent.SkillCalls(path, "")

	require.NoError(t, err)
	assert.Equal(t, []agent.Call{{Line: 11, Text: oracle}}, got)
}

func TestSkillCalls_ACallHeldTwiceInTheTranscriptCountsOnce(t *testing.T) {
	t.Parallel()

	path := writeFile(t, lines(agentSkillLine(t, blast, "a1"), blobLine(t, blast, "a1"), blobLine(t, blast, "a1")))

	got, err := agent.SkillCalls(path, "")

	require.NoError(t, err)
	assert.Equal(t, []agent.Call{{Line: 1, Text: blast}}, got)
}

func TestSkillCalls_WithAnAgentIDOnlyThatAgentCounts(t *testing.T) {
	t.Parallel()

	path := writeFile(t, lines(agentSkillLine(t, blast, "a1"), agentSkillLine(t, oracle, "a2"), skillLine(t, "main-line")))

	got, err := agent.SkillCalls(path, "a2")

	require.NoError(t, err)
	assert.Equal(t, []agent.Call{{Line: 2, Text: oracle}}, got)
}

func TestCheckSkills(t *testing.T) {
	t.Parallel()

	path := writeFile(t, lines(skillLine(t, blast), skillLine(t, blast), skillLine(t, oracle), skillLine(t, "other"), skillLine(t, "blast-radius-review-extra")))

	cases := []struct {
		name     string
		required []string
		want     []string
		ok       bool
	}{
		{"counts_each_in_the_order_given", []string{oracle, blast}, []string{oracle + ": 1", blast + ": 2"}, true},
		{
			"names_each_missing_skill_after_the_counts",
			[]string{blast, "absent", "gone"},
			[]string{blast + ": 2", "absent: 0", "gone: 0", "FAIL: no Skill call for absent", "FAIL: no Skill call for gone"},
			false,
		},
		{"a_name_that_is_part_of_a_called_skill_does_not_count", []string{"blast-radius"}, []string{"blast-radius: 0", "FAIL: no Skill call for blast-radius"}, false},
		{"no_skill_required", nil, []string{"no skill is required"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok, err := agent.CheckSkills(path, tc.required, "")

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.ok, ok)
		})
	}
}

func TestCheckSkills_AMissingTranscriptIsARefusalEvenWhenNoSkillIsRequired(t *testing.T) {
	t.Parallel()

	_, ok, err := agent.CheckSkills(filepath.Join(t.TempDir(), "none"), nil, "")

	require.ErrorIs(t, err, agent.ErrUnreadable)
	assert.False(t, ok)
}

func TestCheckSkills_AMentionInTextDoesNotCount(t *testing.T) {
	t.Parallel()

	text := toJSON(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "I ran " + blast}}}})

	got, ok, err := agent.CheckSkills(writeFile(t, text+"\n"), []string{blast}, "")

	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, []string{blast + ": 0", "FAIL: no Skill call for " + blast}, got)
}

// fakeScan flags a command that holds "git add -A" and one that holds "rm", twice for "rm a; rm b".
func fakeScan(command string) []model.Finding {
	var found []model.Finding

	switch command {
	case "git add -A":
		found = append(found, model.Finding{Kind: model.FindingBulkStaging, Text: command})
	case "ssh h rm a; rm b":
		found = append(found, model.Finding{Kind: model.FindingRemoteDelete, Text: "rm a"}, model.Finding{Kind: model.FindingRemoteDelete, Text: "rm b"})
	case "long":
		found = append(found, model.Finding{Kind: model.FindingLocalDelete, Text: "rm  \t" + repeat("é", 400)})
	}

	return found
}

func repeat(text string, count int) string { return strings.Repeat(text, count) }

func TestCheckTranscript(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{"names_the_line_of_a_hit", lines("plain text", bashLine(t, "git add -A")), []string{"HIT line 2: bulk-staging: git add -A"}},
		{
			"one_call_with_two_findings_prints_two_lines", lines(bashLine(t, "ssh h rm a; rm b")),
			[]string{"HIT line 1: remote-delete: rm a", "HIT line 1: remote-delete: rm b"},
		},
		{"a_clean_transcript_prints_nothing", lines(bashLine(t, "git status")), nil},
		{"cuts_the_text_to_160_characters_and_collapses_blanks", lines("x", bashLine(t, "long")), []string{"HIT line 2: local-delete: rm " + repeat("é", 157)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := agent.CheckTranscript(writeFile(t, tc.content), fakeScan)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCheckTranscript_AMissingTranscriptIsARefusal(t *testing.T) {
	t.Parallel()

	_, err := agent.CheckTranscript(filepath.Join(t.TempDir(), "none"), fakeScan)

	require.ErrorIs(t, err, agent.ErrUnreadable)
}

func TestTranscriptReaders_RefuseAFileWithNoJSONObjectLine(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":           "",
		"only_newlines":   "\n\n\n",
		"not_json":        "plain text\nmore text\n",
		"truncated":       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git ad`,
		"only_non_object": "[1,2]\n\"text\"\n42\n",
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := writeFile(t, content)
			_, bashErr := agent.BashCalls(path)
			_, skillErr := agent.SkillCalls(path, "")
			_, transcriptErr := agent.CheckTranscript(path, fakeScan)
			_, _, checkErr := agent.CheckSkills(path, nil, "")

			for _, err := range []error{bashErr, skillErr, transcriptErr, checkErr} {
				require.ErrorIs(t, err, agent.ErrUnreadable)
				assert.Contains(t, err.Error(), "no line is a JSON object")
			}
		})
	}
}

func TestTranscriptReaders_AcceptOneObjectLineAmongBrokenOnes(t *testing.T) {
	t.Parallel()

	got, err := agent.CheckTranscript(writeFile(t, lines("{broken", "{}", "[1]")), fakeScan)

	require.NoError(t, err)
	assert.Empty(t, got)
}
