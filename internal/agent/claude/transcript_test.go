package claude_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/agent/agenttest"
	"github.com/tobocop2/obee/internal/agent/claude"
	"github.com/tobocop2/obee/internal/model"
)

const (
	blast  = "blast-radius-review"
	oracle = "diff-against-oracle"
)

func TestLine_ReadsTheSessionFromInitAndTheMessageFromResult(t *testing.T) {
	t.Parallel()

	adapter := claude.New()

	session, text := adapter.Line(`{"type":"system","subtype":"init","session_id":"s1","result":"ignored"}`)
	assert.Equal(t, "s1", session)
	assert.Empty(t, text)

	session, text = adapter.Line(`{"type":"result","subtype":"success","session_id":"s1","result":"done"}`)
	assert.Equal(t, "s1", session)
	assert.Equal(t, "done", text)

	for _, other := range []string{`{"type":"system","subtype":"hook","session_id":"s2"}`, `{"type":"assistant"}`, "not json", ""} {
		session, text = adapter.Line(other)
		assert.Empty(t, session, other)
		assert.Empty(t, text, other)
	}
}

func TestTranscriptPath_TurnsEachOtherCharacterOfTheFolderIntoADash(t *testing.T) {
	t.Parallel()

	got := claude.New().TranscriptPath("/login", "/work/My.Tree_1", "abc")

	assert.Equal(t, "/login/projects/-work-My-Tree-1/abc.jsonl", got)
}

func TestProfile_NamesTheModelAndResumeFlags(t *testing.T) {
	t.Parallel()

	p := claude.New().Profile()

	assert.Equal(t, "--model", p.ModelFlag)
	assert.Equal(t, "--resume", p.ResumeFlag)
}

func TestAdapter(t *testing.T) {
	t.Parallel()

	agenttest.TestAdapter(t, func() agent.Adapter { return claude.New() })
}

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

// lines joins transcript lines with a newline after each.
func lines(parts ...string) string {
	return strings.Join(parts, "\n") + "\n"
}

func writeFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "lane.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

func TestBashCalls_ReadsOnlyBashCommandsAndNumbersTheLines(t *testing.T) {
	t.Parallel()

	other := toJSON(t, map[string]any{"message": map[string]any{"content": []any{toolUse("Read", "command", "git add -A")}}})
	noString := toJSON(t, map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": nil}}}}})
	noInput := toJSON(t, map[string]any{"name": "Bash"})
	text := toJSON(t, map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "git add -A"}}}})
	array := toJSON(t, []any{map[string]any{"name": "Bash", "input": map[string]any{"command": "git status"}}})
	path := writeFile(t, lines("not json", "", `{"type":"user","message":{"content":"git add -A"}}`, other, noString, noInput, text, "[1,2]", "{broken", bashLine(t, "ls"), array))

	got, err := claude.New().BashCalls(path)

	require.NoError(t, err)
	assert.Equal(t, []model.Call{{Line: 10, Text: "ls"}, {Line: 11, Text: "git status"}}, got)
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

			got, err := claude.New().BashCalls(writeFile(t, content))

			require.NoError(t, err)
			assert.Equal(t, []model.Call{{Line: 1, Text: "a"}, {Line: 2, Text: "b"}}, got)
		})
	}
}

func TestBashCalls_ABlankLineBetweenTwoCRsStillCountsAsALine(t *testing.T) {
	t.Parallel()

	got, err := claude.New().BashCalls(writeFile(t, "\r\r"+bashLine(t, "x")))

	require.NoError(t, err)
	assert.Equal(t, []model.Call{{Line: 3, Text: "x"}}, got)
}

func TestBashCalls_TwoCallsOnOneLineKeepTheirOrder(t *testing.T) {
	t.Parallel()

	line := toJSON(t, map[string]any{"message": map[string]any{"content": []any{toolUse("Bash", "command", "first"), toolUse("Bash", "command", "second")}}})

	got, err := claude.New().BashCalls(writeFile(t, line+"\n"))

	require.NoError(t, err)
	assert.Equal(t, []model.Call{{Line: 1, Text: "first"}, {Line: 1, Text: "second"}}, got)
}

func TestReadCalls_RefuseAnUnreadableFile(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "none.jsonl")

	_, bashErr := claude.New().BashCalls(missing)
	_, skillErr := claude.New().SkillCalls(missing, "")

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

	_, err = claude.New().BashCalls(path)

	require.ErrorIs(t, err, agent.ErrUnreadable)
	assert.Contains(t, err.Error(), "token too long")
}

func TestReadCalls_AcceptOneObjectLineAmongBrokenOnes(t *testing.T) {
	t.Parallel()

	path := writeFile(t, lines("{broken", "{}", "[1]"))

	bash, bashErr := claude.New().BashCalls(path)
	skills, skillErr := claude.New().SkillCalls(path, "")

	require.NoError(t, bashErr)
	require.NoError(t, skillErr)
	assert.Empty(t, bash)
	assert.Empty(t, skills)
}

func TestReadCalls_RefuseAFileWithNoJSONObjectLine(t *testing.T) {
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
			_, bashErr := claude.New().BashCalls(path)
			_, skillErr := claude.New().SkillCalls(path, "")

			for _, err := range []error{bashErr, skillErr} {
				require.ErrorIs(t, err, agent.ErrUnreadable)
				assert.Contains(t, err.Error(), "no line is a JSON object")
			}
		})
	}
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

	got, err := claude.New().SkillCalls(path, "")

	require.NoError(t, err)
	assert.Equal(t, []model.Call{{Line: 11, Text: oracle}}, got)
}

func TestSkillCalls_ACallHeldTwiceInTheTranscriptCountsOnce(t *testing.T) {
	t.Parallel()

	path := writeFile(t, lines(agentSkillLine(t, blast, "a1"), blobLine(t, blast, "a1"), blobLine(t, blast, "a1")))

	got, err := claude.New().SkillCalls(path, "")

	require.NoError(t, err)
	assert.Equal(t, []model.Call{{Line: 1, Text: blast}}, got)
}

func TestSkillCalls_WithAnAgentIDOnlyThatAgentCounts(t *testing.T) {
	t.Parallel()

	path := writeFile(t, lines(agentSkillLine(t, blast, "a1"), agentSkillLine(t, oracle, "a2"), skillLine(t, "main-line")))

	got, err := claude.New().SkillCalls(path, "a2")

	require.NoError(t, err)
	assert.Equal(t, []model.Call{{Line: 2, Text: oracle}}, got)
}
