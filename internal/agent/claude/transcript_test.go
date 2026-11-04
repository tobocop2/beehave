package claude_test

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/agenttest"
	"github.com/tobocop2/beehave/internal/agent/claude"
)

const (
	blast  = "first-skill"
	oracle = "second-skill"
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
	assert.Equal(t, []string{"--resume", "sess-1"}, p.Resume("sess-1"))
	assert.False(t, p.ResumeLast, "the resume flag keeps its place before the caps")
}

func TestProfile_AcceptsEditsInTheWorkFolderAndRunsBashCalls(t *testing.T) {
	t.Parallel()

	p := claude.New().Profile()

	assert.Equal(t, "--permission-mode acceptEdits", strings.Join(p.EditArgs, " "))
	assert.Contains(t, strings.Join(p.Args, " "), "--allowedTools Bash")
	assert.NotContains(t, strings.Join(p.Args, " "), "--permission-mode")
}

func TestProfile_NamesTheTurnAndBudgetFlags(t *testing.T) {
	t.Parallel()

	p := claude.New().Profile()

	assert.Equal(t, "--max-turns", p.TurnsFlag)
	assert.Equal(t, "--max-budget-usd", p.BudgetFlag)
}

func TestCalls_AnErrorResultThatIsNoCapNamesItsSubtype(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, line, want string
	}{
		{"error", `{"type":"result","subtype":"error_during_execution","is_error":true}`, "error_during_execution"},
		{"error with no subtype", `{"type":"result","is_error":true}`, "unknown"},
		{"turn cap", `{"type":"result","subtype":"error_max_turns","is_error":true}`, ""},
		{"budget cap", `{"type":"result","subtype":"error_max_budget_usd","is_error":true}`, ""},
		{"success", `{"type":"result","subtype":"success","is_error":false}`, ""},
		{"an error subtype with no is_error", `{"type":"result","subtype":"error_during_execution"}`, ""},
		{"not a result line", `{"type":"assistant","is_error":true}`, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, claude.New().Calls(tc.line).Failed)
		})
	}
}

func TestCalls_TheSubtypeOfTheResultLineNamesTheCapThatStoppedTheRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, line string
		want       agent.Stop
	}{
		{"turns", `{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":5}`, agent.StopTurns},
		{"budget", `{"type":"result","subtype":"error_max_budget_usd","is_error":true}`, agent.StopBudget},
		{"success", `{"type":"result","subtype":"success","is_error":false}`, ""},
		{"other error", `{"type":"result","subtype":"error_during_execution"}`, ""},
		{"not a result line", `{"type":"assistant","subtype":"error_max_turns"}`, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, claude.New().Calls(tc.line).Stop)
		})
	}
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

func toolUse(tool, key string, value any) map[string]any {
	return map[string]any{"type": "tool_use", "id": "toolu_1", "name": tool, "input": map[string]any{key: value}}
}

func assistant(t *testing.T, blocks ...any) string {
	t.Helper()

	return toJSON(t, map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": blocks}})
}

func TestCalls_ReadTheStreamOfOneRun(t *testing.T) {
	t.Parallel()

	file, err := os.Open("testdata/stream.jsonl")
	require.NoError(t, err)

	defer file.Close()

	var shell, skills []string

	results := 0

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		seen := claude.New().Calls(scanner.Text())
		shell, skills = append(shell, texts(seen.Shell)...), append(skills, seen.Skills...)

		assert.False(t, seen.Invalid, scanner.Text())
		assert.Empty(t, seen.Refused, scanner.Text())

		if seen.Result {
			results++
		}
	}

	require.NoError(t, scanner.Err())
	assert.Equal(t, []string{"git status", "git add -A"}, shell, "the sub-agent call counts and the text of a tool_result does not")
	assert.Equal(t, []string{blast, oracle}, skills)
	assert.Equal(t, 1, results, "one line ends the run")
}

func texts(calls []agent.Call) []string {
	var out []string
	for _, call := range calls {
		out = append(out, call.Text)
	}

	return out
}

// The fixture of the reviewer: a Bash call, then the tool_result that the PreToolUse hook produced when it refused the call.
const (
	refusedCall   = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_9","name":"Bash","input":{"command":"git add -A"}}]}}`
	refusedResult = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_9","is_error":true,"content":"PreToolUse:Bash hook error: [beehave check command]: beehave refused the command: bulk-staging: git add -A"}]}}`
)

func TestCalls_ACallCarriesItsIdAndAHookRefusalNamesTheSameId(t *testing.T) {
	t.Parallel()

	call := claude.New().Calls(refusedCall)
	assert.Equal(t, []agent.Call{{ID: "toolu_9", Text: "git add -A"}}, call.Shell)
	assert.Empty(t, call.Refused)

	refusal := claude.New().Calls(refusedResult)
	assert.Equal(t, []string{"toolu_9"}, refusal.Refused)
	assert.Empty(t, refusal.Shell)
}

func TestCalls_OnlyAnErrorResultOfAHookIsARefusal(t *testing.T) {
	t.Parallel()

	block := func(isError bool, content any) string {
		return toJSON(t, map[string]any{"type": "user", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "is_error": isError, "content": content},
		}}})
	}
	listed := []any{map[string]any{"type": "text", "text": "PreToolUse:Bash hook error: no"}}

	assert.Equal(t, []string{"t1"}, claude.New().Calls(block(true, listed)).Refused, "a list of text blocks")

	for name, line := range map[string]string{
		"no error":        block(false, "PreToolUse:Bash hook error"),
		"other error":     block(true, "exit status 1"),
		"empty list":      block(true, []any{}),
		"not text":        block(true, []any{map[string]any{"type": "image"}, "x"}),
		"number":          block(true, 5),
		"assistant line":  strings.Replace(block(true, "PreToolUse:x"), `"user"`, `"assistant"`, 1),
		"no tool_use_id":  strings.Replace(block(true, "PreToolUse:x"), `"tool_use_id":"t1",`, "", 1),
		"no message":      `{"type":"user"}`,
		"content a plain": `{"type":"user","message":{"content":"PreToolUse:x"}}`,
		"block a string":  `{"type":"user","message":{"content":["PreToolUse:x"]}}`,
	} {
		assert.Empty(t, claude.New().Calls(line).Refused, name)
	}
}

func TestCalls_OnlyTheResultLineEndsTheRun(t *testing.T) {
	t.Parallel()

	assert.True(t, claude.New().Calls(`{"type":"result","subtype":"success","session_id":"s1"}`).Result)
	assert.False(t, claude.New().Calls(`{"type":"assistant"}`).Result)
	assert.False(t, claude.New().Calls(`[1]`).Result)
}

func TestCalls_ALineThatIsNotJSONIsInvalidAndAnEmptyLineIsNot(t *testing.T) {
	t.Parallel()

	for _, line := range []string{"plain text", "{broken", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git ad`} {
		assert.True(t, claude.New().Calls(line).Invalid, line)
	}

	for _, line := range []string{"", "  ", "[1]", `"text"`, `{}`} {
		assert.False(t, claude.New().Calls(line).Invalid, line)
	}
}

func TestCalls_ABashCallCountsOnEveryLineShapeAndKeepsItsOrder(t *testing.T) {
	t.Parallel()

	first := toolUse("Bash", "command", "first")
	second := toolUse("Bash", "command", "second")
	bare := toJSON(t, map[string]any{"message": map[string]any{"content": []any{first, second}}})
	other := toJSON(t, map[string]any{"message": map[string]any{"content": []any{toolUse("Read", "command", "git add -A")}}})
	noString := toJSON(t, map[string]any{"message": map[string]any{"content": []any{toolUse("Bash", "command", nil)}}})

	seen := claude.New().Calls(bare)
	assert.Equal(t, []string{"first", "second"}, texts(seen.Shell))
	assert.Empty(t, seen.Skills)

	for _, line := range []string{other, noString} {
		assert.Empty(t, claude.New().Calls(line).Shell, line)
	}
}

func TestCalls_ALineThatIsNotAJSONObjectHoldsNoCall(t *testing.T) {
	t.Parallel()

	for _, line := range []string{"", "plain text", "{broken", "[1]", `"text"`, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git ad`} {
		seen := claude.New().Calls(line)

		assert.Empty(t, seen.Shell, line)
		assert.Empty(t, seen.Skills, line)
	}
}

func TestCalls_ASkillCountsOnlyAsAToolUseBlockOfAnAssistantLine(t *testing.T) {
	t.Parallel()

	shaped := toolUse("Skill", "skill", blast)
	result := toJSON(t, map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "content": []any{shaped}}}}})
	extra := toJSON(t, map[string]any{"type": "user", "toolUseResult": shaped})
	text := assistant(t, map[string]any{"type": "text", "text": "I used " + blast})
	noContent := toJSON(t, map[string]any{"type": "assistant", "message": map[string]any{"content": "plain"}})
	noMessage := toJSON(t, map[string]any{"type": "assistant"})
	notBlock := assistant(t, "text", 5)
	wrongTool := assistant(t, toolUse("Read", "skill", blast))
	notToolUse := assistant(t, map[string]any{"type": "tool_result", "name": "Skill", "input": map[string]any{"skill": blast}})
	notString := assistant(t, toolUse("Skill", "skill", 5))

	for _, line := range []string{result, extra, text, noContent, noMessage, notBlock, wrongTool, notToolUse, notString} {
		assert.Empty(t, claude.New().Calls(line).Skills, line)
	}

	assert.Equal(t, []string{oracle}, claude.New().Calls(assistant(t, toolUse("Skill", "skill", oracle))).Skills)
}
