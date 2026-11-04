package transcript_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/transcript"
)

func TestLaneContext_IsTheContextFieldOfTheLaneTranscriptLine(t *testing.T) {
	t.Parallel()

	row := `{"message":{"id":"m1","role":"assistant","usage":{"input_tokens":10,"cache_read_input_tokens":40000,"cache_creation_input_tokens":5000,"output_tokens":200}}}`
	path := writeFile(t, "lane.jsonl", row+"\n")

	line, err := transcript.LaneTranscriptLine(path)
	require.NoError(t, err)

	var want string

	for _, field := range strings.Split(line, tab) {
		if value, ok := strings.CutPrefix(field, "context="); ok {
			want = value
		}
	}

	got, err := transcript.LaneContext(path)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestLaneContext_RefusesATranscriptItCannotRead(t *testing.T) {
	t.Parallel()

	_, err := transcript.LaneContext(filepath.Join(t.TempDir(), "none"))
	require.ErrorContains(t, err, "cannot read the lane transcript")
}
