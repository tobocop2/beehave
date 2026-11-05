package opencode_test

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/opencode"
)

// fixtureLines returns the lines of a file in testdata.
func fixtureLines(t *testing.T, name string) []string {
	t.Helper()

	raw, err := fs.ReadFile(os.DirFS("testdata"), name)
	require.NoError(t, err)

	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// total is what the caller learns from a whole stream.
type total struct {
	session, text string
	spent         float64
	usage         agent.Tokens
	commands      []string
	refused       []string
	failed        string
	result        bool
	priced        bool
}

func read(t *testing.T, lines []string) total {
	t.Helper()

	var got total

	adapter := opencode.New()

	for _, line := range lines {
		session, text := adapter.Line(line)
		if session != "" {
			got.session = session
		}

		if text != "" {
			got.text = text
		}

		seen := adapter.Calls(line)
		got.spent += seen.Spent
		got.usage = got.usage.Add(seen.Usage)
		got.refused = append(got.refused, seen.Refused...)
		got.result = got.result || seen.Result
		got.priced = got.priced || seen.Priced

		got.failed = seen.Failed + got.failed

		for _, call := range seen.Shell {
			got.commands = append(got.commands, call.Text)
		}

		assert.False(t, seen.Invalid, line)
		assert.Empty(t, seen.Skills, line)
	}

	return got
}

func TestStream_ReadsTheSessionTheLastTextTheDollarsAndTheTokens(t *testing.T) {
	t.Parallel()

	got := read(t, fixtureLines(t, "ok.jsonl"))

	assert.Equal(t, "ses_e56bc95d1ffeLiRaZ6Ozp5CEin", got.session)
	assert.Equal(t, "done", got.text)
	assert.InDelta(t, 0.000046, got.spent, 1e-12)
	assert.Equal(t, agent.Tokens{Input: 30, Output: 8}, got.usage)
	assert.Equal(t, []string{"echo hi"}, got.commands)
	assert.Empty(t, got.refused)
	assert.Empty(t, got.failed)
	assert.True(t, got.result)
	assert.True(t, got.priced)
}

func TestStream_ACutStreamHasNoResultLine(t *testing.T) {
	t.Parallel()

	got := read(t, fixtureLines(t, "ok.jsonl")[:3])

	assert.False(t, got.result, "a step that ends in tool-calls is not the end of the run")
	assert.Equal(t, []string{"echo hi"}, got.commands)
}

func TestStream_ARefusedCallIsRefusedAndStillShown(t *testing.T) {
	t.Parallel()

	got := read(t, fixtureLines(t, "refused.jsonl"))

	assert.Equal(t, []string{"call_1"}, got.refused)
	assert.Equal(t, []string{"touch /home/jobs/ocs1/work/forbidden-ran"}, got.commands)
	assert.True(t, got.result)
	assert.True(t, got.priced)
	assert.Empty(t, got.failed, "a refused call is no failed run: the model went on and answered")
}

func TestStream_ACallThatRanWithNoPluginIsNotRefused(t *testing.T) {
	t.Parallel()

	got := read(t, fixtureLines(t, "noguard.jsonl"))

	assert.Empty(t, got.refused, "nothing in the output of a run with no plugin tells it from a run with a guard that let the call through")
	assert.Equal(t, []string{"touch /home/jobs/ocs1/work/forbidden-ran"}, got.commands)
}

func TestCalls_AnErrorOfAToolThatIsNotARefusalIsNotRefused(t *testing.T) {
	t.Parallel()

	line := `{"type":"tool_use","sessionID":"s","part":{"callID":"c","tool":"bash","state":{"status":"error","input":{"command":"false"},"error":"exit 1"}}}`

	seen := opencode.New().Calls(line)

	assert.Empty(t, seen.Refused)
	assert.Equal(t, []agent.Call{{ID: "c", Text: "false"}}, seen.Shell)
}

func TestCalls_OtherToolsAreNoShellCalls(t *testing.T) {
	t.Parallel()

	line := `{"type":"tool_use","sessionID":"s","part":{"callID":"c","tool":"read","state":{"status":"completed","input":{"filePath":"a"}}}}`

	assert.Empty(t, opencode.New().Calls(line).Shell)
}

func TestCalls_AnErrorLineSetsFailed(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, line, want string }{
		{"recorded unknown model", fixtureLines(t, "error.jsonl")[0], "UnknownError: Unexpected server error. Check server logs for details."},
		{"named with a message", `{"type":"error","sessionID":"s","error":{"name":"APIError","data":{"message":"Not Found","statusCode":404}}}`, "APIError: Not Found"},
		{"named with no message", `{"type":"error","sessionID":"s","error":{"name":"APIError"}}`, "APIError"},
		{"a plain string", `{"type":"error","sessionID":"s","error":"quota"}`, "quota"},
		{"a shape that is not known", `{"type":"error","sessionID":"s","error":5}`, "unknown"},
		{"unnamed", `{"type":"error","sessionID":"s"}`, "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, opencode.New().Calls(tc.line).Failed)
		})
	}
}

func TestCalls_AStepThatEndsInAFailureReasonFailsTheRun(t *testing.T) {
	t.Parallel()

	for reason, failed := range map[string]bool{"error": true, "length": true, "content-filter": true, "stop": false, "tool-calls": false, "unknown": false} {
		seen := opencode.New().Calls(`{"type":"step_finish","sessionID":"s","part":{"reason":"` + reason + `"}}`)

		assert.Equal(t, failed, seen.Failed != "", reason)
		assert.Equal(t, reason != "tool-calls", seen.Result, reason)
	}
}

func TestCalls_ALineThatIsNotJSONIsInvalidAndABlankLineIsNot(t *testing.T) {
	t.Parallel()

	assert.True(t, opencode.New().Calls("not json").Invalid)
	assert.False(t, opencode.New().Calls("  ").Invalid)
}

func TestCalls_AStepFinishWithNoCostFieldIsNotPriced(t *testing.T) {
	t.Parallel()

	seen := opencode.New().Calls(`{"type":"step_finish","sessionID":"s","part":{"reason":"stop"}}`)

	assert.False(t, seen.Priced)
	assert.True(t, seen.Result)
}

func TestCalls_ANegativeOrNonNumberCostAddsNothing(t *testing.T) {
	t.Parallel()

	for _, cost := range []string{"-1", `"lots"`} {
		seen := opencode.New().Calls(`{"type":"step_finish","sessionID":"s","part":{"reason":"stop","cost":` + cost + `}}`)

		assert.InDelta(t, 0, seen.Spent, 0, cost)
	}
}

func TestLine_AnyOtherLineHoldsNoText(t *testing.T) {
	t.Parallel()

	session, text := opencode.New().Line(`{"type":"step_start","sessionID":"ses_7"}`)

	assert.Equal(t, "ses_7", session)
	assert.Empty(t, text)

	session, text = opencode.New().Line("not json")
	assert.Empty(t, session)
	assert.Empty(t, text)
}
