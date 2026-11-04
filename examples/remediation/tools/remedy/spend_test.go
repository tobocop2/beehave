package remedy_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/examples/remediation/tools/remedy"
)

func callLine(clock string, cacheRead int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-09T%s:00.000Z","message":{"role":"assistant","usage":{"cache_read_input_tokens":%d,"output_tokens":10}}}`+"\n", clock, cacheRead)
}

func noteLine(clock, task, desc string, tokens int) string {
	content := fmt.Sprintf(`<task-notification>\n<task-id>%s</task-id>\n<summary>Agent \"%s\" finished</summary>\n<usage><subagent_tokens>%d</subagent_tokens><tool_uses>5</tool_uses></usage>`, task, desc, tokens)

	return fmt.Sprintf(`{"type":"user","timestamp":"2026-10-09T%s:00.000Z","message":{"role":"user","content":"%s"}}`+"\n", clock, content)
}

func TestSpend_AnUnderCapLaneAndTheHourTablePass(t *testing.T) {
	t.Parallel()

	path := textFile(t, "t.jsonl", callLine("09:05", 1)+noteLine("09:40", "abc1", "Lane fix-9 implement", 80000))

	code, out, errOut := run(t, remedy.Deps{}, "spend", path)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "hour           calls")
	assert.Contains(t, out, "abc1\tLane fix-9 implement\t80000\tunder")
	assert.Empty(t, errOut)
}

func TestSpend_ALaneOverTheCapIsExitCodeOne(t *testing.T) {
	t.Parallel()

	path := textFile(t, "t.jsonl", noteLine("09:40", "abc1", "Lane fix-9 implement", 200000))

	code, out, _ := run(t, remedy.Deps{}, "spend", path)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "abc1\tLane fix-9 implement\t200000\tOVER")
}

func TestSpend_TheCapFlagMovesTheLine(t *testing.T) {
	t.Parallel()

	path := textFile(t, "t.jsonl", noteLine("09:40", "abc1", "Lane fix-9 implement", 80000))

	code, out, _ := run(t, remedy.Deps{}, "spend", path, "--cap", "50000")

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "80000\tOVER")
}

func TestSpend_TheHoursFlagKeepsTheLastHoursOfTheTable(t *testing.T) {
	t.Parallel()

	path := textFile(t, "t.jsonl", callLine("09:05", 1)+callLine("10:05", 1))

	_, all, _ := run(t, remedy.Deps{}, "spend", path)
	_, last, _ := run(t, remedy.Deps{}, "spend", path, "--hours", "1")

	assert.Contains(t, all, "T09")
	assert.NotContains(t, last, "T09")
	assert.Contains(t, last, "T10")
}

func TestSpend_ABadCapAnAbsentTranscriptAndABadCallAreExitCodeTwo(t *testing.T) {
	t.Parallel()

	good := textFile(t, "t.jsonl", callLine("09:05", 1))

	cases := map[string][]string{
		"bad cap":        {"spend", good, "--cap", "0"},
		"absent file":    {"spend", filepath.Join(t.TempDir(), "nope.jsonl")},
		"no argument":    {"spend"},
		"extra argument": {"spend", good, "extra"},
		"unknown flag":   {"spend", good, "--nope"},
	}

	for name, args := range cases {
		code, _, errOut := run(t, remedy.Deps{}, args...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.NotEmpty(t, errOut, name)
	}
}
