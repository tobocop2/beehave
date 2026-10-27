package transcript_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/check/transcript"
	"github.com/tobocop2/obee/internal/model"
)

const (
	blast  = "blast-radius-review"
	oracle = "diff-against-oracle"
)

var errUnreadable = errors.New("cannot read the transcript")

// skillReader returns a reader that yields one Skill call for each name, on lines 1, 2 and so on.
func skillReader(names ...string) func(path, agentID string) ([]model.Call, error) {
	return func(string, string) ([]model.Call, error) {
		calls := make([]model.Call, len(names))
		for i, name := range names {
			calls[i] = model.Call{Line: i + 1, Text: name}
		}

		return calls, nil
	}
}

// bashReader returns a reader that yields the given calls.
func bashReader(calls ...model.Call) func(path string) ([]model.Call, error) {
	return func(string) ([]model.Call, error) { return calls, nil }
}

func TestSkills(t *testing.T) {
	t.Parallel()

	read := skillReader(blast, blast, oracle, "other", "blast-radius-review-extra")

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

			got, ok, err := transcript.Skills(read, "lane.jsonl", tc.required, "")

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.ok, ok)
		})
	}
}

func TestSkills_PassesThePathAndTheAgentToTheReader(t *testing.T) {
	t.Parallel()

	var gotPath, gotAgent string

	read := func(path, agentID string) ([]model.Call, error) {
		gotPath, gotAgent = path, agentID

		return nil, nil
	}

	_, _, err := transcript.Skills(read, "lane.jsonl", nil, "agent-7")

	require.NoError(t, err)
	assert.Equal(t, "lane.jsonl", gotPath)
	assert.Equal(t, "agent-7", gotAgent)
}

func TestSkills_AReaderErrorIsARefusalEvenWhenNoSkillIsRequired(t *testing.T) {
	t.Parallel()

	read := func(string, string) ([]model.Call, error) { return nil, errUnreadable }

	_, ok, err := transcript.Skills(read, "none", nil, "")

	require.ErrorIs(t, err, errUnreadable)
	assert.False(t, ok)
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

func TestBash(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		calls []model.Call
		want  []string
	}{
		{"names_the_line_of_a_hit", []model.Call{{Line: 2, Text: "git add -A"}}, []string{"HIT line 2: bulk-staging: git add -A"}},
		{
			"one_call_with_two_findings_prints_two_lines",
			[]model.Call{{Line: 1, Text: "ssh h rm a; rm b"}},
			[]string{"HIT line 1: remote-delete: rm a", "HIT line 1: remote-delete: rm b"},
		},
		{"a_clean_transcript_prints_nothing", []model.Call{{Line: 1, Text: "git status"}}, nil},
		{"cuts_the_text_to_160_characters_and_collapses_blanks", []model.Call{{Line: 2, Text: "long"}}, []string{"HIT line 2: local-delete: rm " + repeat("é", 157)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := transcript.Bash(bashReader(tc.calls...), "lane.jsonl", fakeScan)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBash_AReaderErrorIsARefusal(t *testing.T) {
	t.Parallel()

	read := func(string) ([]model.Call, error) { return nil, errUnreadable }

	_, err := transcript.Bash(read, "none", fakeScan)

	require.ErrorIs(t, err, errUnreadable)
}
