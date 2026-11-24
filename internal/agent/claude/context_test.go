package claude_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/claude"
)

func TestCalls_TheRecordedStreamReportsTheContextOfEachTurnOfTheMainAgent(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("testdata", "context-stream.jsonl"))
	require.NoError(t, err)

	var got []int64
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		got = append(got, claude.New(nil).Calls(line).Context)
	}

	// init line, turn 1, a sub-agent turn that counts for nothing, turn 2, turn 3, result line.
	assert.Equal(t, []int64{0, 25010, 0, 170012, 190005, 0}, got)
}

func TestCalls_AnAssistantLineWithNoUsableUsageReportsNoContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, line string
		want       int64
	}{
		{"no usage", `{"type":"assistant","message":{"content":[]}}`, 0},
		{"a count that is no number", `{"type":"assistant","message":{"usage":{"input_tokens":"many","cache_read_input_tokens":7}}}`, 7},
		{"a negative count", `{"type":"assistant","message":{"usage":{"input_tokens":-5,"cache_read_input_tokens":7}}}`, 7},
		{"not an assistant line", `{"type":"user","message":{"usage":{"input_tokens":9}}}`, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, claude.New(nil).Calls(tc.line).Context)
		})
	}
}
